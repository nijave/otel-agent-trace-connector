// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"go/ast"
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

// TestNoCrossHarnessImports fails when a harness package uses anything from
// another harness package except its Claimer, and when a harness package that
// another imports does not declare `var Claimer canonical.Claimer`.
func TestNoCrossHarnessImports(t *testing.T) {
	packages := harnessPackages(t)

	for _, shared := range sortedKeys(sharedPackages) {
		require.DirExistsf(t, filepath.Join("internal", shared), "sharedPackages lists %q, which is not an internal package", shared)
	}

	for _, pkg := range packages {
		refs, err := harnessReferences(filepath.Join("internal", pkg))
		require.NoErrorf(t, err, "scan internal/%s", pkg)
		for _, imported := range sortedKeys(refs) {
			require.NotEqualf(t, pkg, imported, "internal/%s imports itself", pkg)
			for _, ident := range sortedKeys(refs[imported]) {
				if ident == claimerName {
					continue
				}
				t.Errorf("internal/%s uses %s.%s from harness package internal/%s.\n"+
					"Harness packages may use only another harness's %s (docs/design.md#configuration-structure).\n"+
					"Move the shared helper or constant into internal/canonical or internal/content.",
					pkg, filepath.Base(imported), ident, imported, claimerName)
			}
			declared, err := declaresClaimer(filepath.Join("internal", imported))
			require.NoErrorf(t, err, "scan internal/%s", imported)
			require.Truef(t, declared,
				"internal/%s imports internal/%s, which must declare `var %s canonical.%s`",
				pkg, imported, claimerName, claimerName)
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

// claimerName is the one identifier a harness package may use from another
// harness package (canonical.Claimer, docs/design.md#configuration-structure).
const claimerName = "Claimer"

// harnessReferences returns, for each non-shared internal package that dir's
// Go files import, the identifiers those files select from it, keyed by the
// package path relative to internal/ ("claude" -> {"Claimer"}). A dot or blank
// import is recorded as the identifier "." or "_" so the caller can reject it:
// a dot import hides every use from a selector scan. Selectors on a local
// declaration that shadows the package name are not references.
func harnessReferences(dir string) (map[string]map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	refs := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		local := map[string]string{}
		for _, spec := range file.Imports {
			imported, ok := strings.CutPrefix(strings.Trim(spec.Path.Value, `"`), internalPrefix)
			if !ok || sharedPackages[imported] != nil {
				continue
			}
			if refs[imported] == nil {
				refs[imported] = map[string]bool{}
			}
			name := filepath.Base(imported)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." || name == "_" {
				refs[imported][name] = true
				continue
			}
			local[name] = imported
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Obj != nil {
				return true
			}
			if imported, ok := local[ident.Name]; ok {
				refs[imported][sel.Sel.Name] = true
			}
			return true
		})
	}
	return refs, nil
}

// declaresClaimer reports whether dir's non-test Go files declare
// `var Claimer canonical.Claimer`.
func declaresClaimer(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return false, err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				typ, ok := value.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := typ.X.(*ast.Ident)
				if !ok || pkg.Name != "canonical" || typ.Sel.Name != claimerName {
					continue
				}
				for _, n := range value.Names {
					if n.Name == claimerName {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
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
