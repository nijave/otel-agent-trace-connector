// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These guards enforce the configuration structure rules in
// docs/design.md#configuration-structure: the YAML tree is the Go type tree, a
// one-harness key lives in that harness package, and the root Config holds only
// connector-wide keys. Every list below is an allowlist, so adding a key or an
// import forces a deliberate edit here.

// internalPrefix is the import path prefix every component-internal package
// shares.
const internalPrefix = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/"

// sharedPackages are the internal packages that are not harness edges. Any
// harness may import them; every other package under internal/ is a harness.
var sharedPackages = map[string][]string{
	"canonical":    {"canonical vocabulary, resource contract, conformance runner"},
	"content":      {"content stripping"},
	"metadata":     {"mdatagen-generated status and telemetry"},
	"metadatatest": {"mdatagen-generated test helpers"},
}

// crossHarnessImports records every place a harness package may import another
// harness package, keyed by importer and imported package. The only exception
// is the generic GenAI edge, which defers a resource group to the provider edge
// that owns it by calling that provider's Contains*Spans claim predicate.
// Relocating those predicates to internal/canonical would remove the exception;
// choosing between that and the deferral is a maintainer's call, so the
// exception is enumerated here rather than left implicit.
var crossHarnessImports = map[string]map[string]string{
	"genai": {
		"claude":    "ContainsClaudeSpans defers Claude groups",
		"opencode":  "ContainsOpenCodeSpans defers OpenCode groups",
		"openhands": "ContainsOpenHandsSpans defers OpenHands groups",
		"pi":        "ContainsPiSpans defers Pi groups",
	},
}

// harnessBlocks maps a root Config field's mapstructure tag — the YAML key — to
// the package that owns that block.
var harnessBlocks = map[string]string{
	"claude_code": internalPrefix + "claude",
	"codex":       internalPrefix + "codex",
	"cursor":      internalPrefix + "cursor",
}

// connectorWideFields lists every root Config field that is not a harness
// block, with the reason the key is connector-wide.
var connectorWideFields = map[string]string{
	"capture_identity": "every edge applies the same identity policy, so the routers pass it to each edge",
}

// TestNoCrossHarnessImports fails when a harness package imports another
// harness package, and when an enumerated exception is stale.
func TestNoCrossHarnessImports(t *testing.T) {
	packages := harnessPackages(t)

	for _, shared := range sortedKeys(sharedPackages) {
		require.DirExistsf(t, filepath.Join("internal", shared), "sharedPackages lists %q, which is not an internal package", shared)
	}

	importedHarness := map[string]map[string]bool{}
	for _, pkg := range packages {
		dir := filepath.Join("internal", pkg)
		harness := map[string]bool{}
		for _, imported := range internalImports(t, dir) {
			if sharedPackages[imported] != nil {
				continue
			}
			require.NotEqualf(t, pkg, imported, "internal/%s imports itself", pkg)
			harness[imported] = true
			if _, ok := crossHarnessImports[pkg][imported]; ok {
				continue
			}
			t.Errorf("internal/%s imports harness package internal/%s.\n"+
				"Harness packages must not import each other (docs/design.md#configuration-structure).\n"+
				"Move the shared helper or constant into internal/canonical or internal/content, or — "+
				"if the dependency is deliberate and documented — add %q to %q under %q in this test.",
				pkg, imported, imported, "crossHarnessImports", pkg)
		}
		importedHarness[pkg] = harness
	}

	for importer, exceptions := range crossHarnessImports {
		require.Containsf(t, packages, importer, "crossHarnessImports lists %q, which is not a harness package", importer)
		for imported, reason := range exceptions {
			require.NotEmptyf(t, reason, "crossHarnessImports[%q][%q] needs a reason", importer, imported)
			require.Truef(t, importedHarness[importer][imported],
				"crossHarnessImports[%q][%q] is stale: internal/%s no longer imports internal/%s",
				importer, imported, importer, imported)
		}
	}
}

// TestRootConfigNonHarnessFieldsAreConnectorWide fails when a root Config field
// escapes the harness-block table or the connector-wide allowlist, so a new
// key has to be classified rather than landing in the root by default.
func TestRootConfigNonHarnessFieldsAreConnectorWide(t *testing.T) {
	rootType := reflect.TypeOf(Config{})

	tags := map[string]bool{}
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		tag, tagged := field.Tag.Lookup("mapstructure")
		require.Truef(t, tagged, "%s.%s has no mapstructure tag; the YAML tree must mirror the Go type tree", rootType.Name(), field.Name)
		require.NotEmptyf(t, tag, "%s.%s has an empty mapstructure tag", rootType.Name(), field.Name)
		tags[tag] = true

		isHarnessBlock := field.Type.Kind() == reflect.Struct
		if isHarnessBlock {
			require.NotContainsf(t, connectorWideFields, tag,
				"%s.%s (%q) is a struct, so it is a harness block; remove it from connectorWideFields and add it to harnessBlocks", rootType.Name(), field.Name, tag)
			continue
		}

		reason, ok := connectorWideFields[tag]
		require.Truef(t, ok,
			"%s.%s (%q) is a connector-wide key, so it needs an entry in connectorWideFields explaining why more than one harness reads it.\n"+
				"If only one harness reads it, move the field into that harness package's Config and add a harnessBlocks entry instead.",
			rootType.Name(), field.Name, tag)
		require.NotEmptyf(t, reason, "connectorWideFields[%q] needs a reason", tag)
	}

	for tag := range connectorWideFields {
		require.Truef(t, tags[tag], "connectorWideFields lists %q, which root Config no longer has", tag)
	}
}

// TestRootConfigHarnessBlocksMatchTheirPackages fails when a harness block's
// YAML key, type name, or package disagrees with harnessBlocks.
func TestRootConfigHarnessBlocksMatchTheirPackages(t *testing.T) {
	rootType := reflect.TypeOf(Config{})

	seen := map[string]bool{}
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		if field.Type.Kind() != reflect.Struct {
			continue
		}
		tag := field.Tag.Get("mapstructure")
		wantPackage, ok := harnessBlocks[tag]
		require.Truef(t, ok,
			"%s.%s has mapstructure key %q, which is not in harnessBlocks.\n"+
				"Add %q -> the owning package (for example %q) so the YAML nesting keeps matching the Go package nesting.",
			rootType.Name(), field.Name, tag, tag, internalPrefix+"<harness>")
		seen[tag] = true
		require.Equalf(t, "Config", field.Type.Name(), "%s.%s must be the owning package's Config type", rootType.Name(), field.Name)
		require.Equalf(t, wantPackage, field.Type.PkgPath(),
			"%s.%s (%q) decodes into the wrong package: harnessBlocks says %q", rootType.Name(), field.Name, tag, wantPackage)
	}

	for tag := range harnessBlocks {
		require.Truef(t, seen[tag], "harnessBlocks lists %q, which root Config has no struct field for", tag)
	}
}

// harnessPackages returns the internal/ package directories that are harness
// edges: every package with Go files that is not in sharedPackages.
func harnessPackages(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("internal")
	require.NoError(t, err)
	var packages []string
	for _, entry := range entries {
		if !entry.IsDir() || sharedPackages[entry.Name()] != nil {
			continue
		}
		if len(goFiles(t, filepath.Join("internal", entry.Name()))) == 0 {
			continue
		}
		packages = append(packages, entry.Name())
	}
	require.NotEmpty(t, packages, "no harness packages found under internal/")
	sort.Strings(packages)
	return packages
}

// internalImports returns the component-internal packages dir imports, one
// entry per import (paths relative to internal/, e.g. "codex").
func internalImports(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var imports []string
	for _, name := range goFiles(t, dir) {
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		require.NoErrorf(t, err, "parse %s", path)
		for _, spec := range file.Imports {
			if imported, ok := strings.CutPrefix(strings.Trim(spec.Path.Value, `"`), internalPrefix); ok {
				imports = append(imports, imported)
			}
		}
	}
	sort.Strings(imports)
	return imports
}

// goFiles returns the names of dir's Go files.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
