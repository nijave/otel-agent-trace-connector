# Harness Claimer Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the exported `Contains*Spans` claim predicates with one `Claimer` interface that each trace harness implements. Then tighten the cross-harness import guard so `Claimer` is the only thing one harness package may use from another.

**Architecture:** `internal/canonical` defines `Claimer` with a single method, `Claim(ptrace.ResourceSpans) bool`. Each trace harness package exports one value, `var Claimer canonical.Claimer = claimer{}`, and its old predicate becomes unexported behind that value. `internal/genai` keeps importing the four harness packages it defers to, but it references only their `Claimer` values. `TestNoCrossHarnessImports` drops its per-package exception table. It parses each harness package's source and fails on any reference to another harness package other than `<pkg>.Claimer`. It also fails when an imported harness package does not declare `var Claimer canonical.Claimer`.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`), `go/parser` and `go/ast` (standard library) for the guard test, and testify `require`.

**Spec:** the user asked for a `Claim(…) bool` interface that each harness implements, for `internal/genai/normalizer.go` to import the harness packages and call it, and for the guard test to allow that interface and nothing else. No separate design document exists. `docs/design.md#configuration-structure` holds the rule this changes.

## Global Constraints

- No behavior change: every claim decision must stay the same for every input. The existing per-harness predicate tests and the genai `Skips*Groups` tests must keep passing with unchanged assertions; only the called identifiers change.
- The exported names `ContainsClaudeSpans`, `ContainsOpenCodeSpans`, `ContainsPiSpans`, `ContainsOpenHandsSpans`, and `ContainsCodexSpans` no longer exist anywhere outside `docs/superpowers/`.
- Interface name `Claimer`, method `Claim`, and the per-package value `Claimer`, all spelled exactly like that.
- Packages that get a `Claimer` are exactly the five with a span-claim predicate today: `claude`, `opencode`, `pi`, `openhands`, and `codextraces`. Do not add it to `codex`, `cursor`, `claudelogs`, or the shared packages.
- Do not edit anything under `docs/superpowers/`; those are historical plans and specs. This new plan file is the only exception.
- All paths below are relative to the repository root. Go commands run from `connector/codingagentconnector/`, which is its own module.
- The final gate is `./scripts/check.sh` from the repository root, per `AGENTS.md`.

## Review Focus

- **An aliased import of a harness package** (`c "…/internal/claude"`) followed by `c.Helper()` must still fail the guard. The test must resolve the local name from the import spec. Covered by a `harnessReferences` table case in Task 3.
- **A dot or blank import of a harness package** must fail the guard. A dot import would let callers use any identifier unqualified, which hides it from a selector scan. Covered in Task 3.
- **A local variable that shadows a package name**, such as `pi := 3.14` followed by `pi.X`, must not count as a harness reference. Covered in Task 3: only selectors whose identifier has no local declaration (`Ident.Obj == nil`) count.
- **A package that exports `Claimer` with the wrong type**, for example `var Claimer = claimer{}` without the `canonical.Claimer` type, must fail `declaresClaimer`. Covered in Task 3.
- **The order in which genai defers to other edges** must stay Claude, OpenCode, Pi, then OpenHands. Results are the same in any order today, but keeping the order keeps the diff a pure refactor. Task 2 writes the slice in that order.

---

## File Structure

| File | Change | Responsibility |
| --- | --- | --- |
| `connector/codingagentconnector/internal/canonical/claim.go` | create | `Claimer` interface |
| `connector/codingagentconnector/internal/claude/normalizer.go` | change (~L116-130) | unexport the predicate; add `claimer` and `Claimer` |
| `connector/codingagentconnector/internal/opencode/normalizer.go` | change (~L86-96) | same |
| `connector/codingagentconnector/internal/pi/normalizer.go` | change (~L95-110) | same |
| `connector/codingagentconnector/internal/openhands/normalizer.go` | change (~L140-160) | same |
| `connector/codingagentconnector/internal/codextraces/normalizer.go` | change (~L149-190) | same |
| `connector/codingagentconnector/internal/opencode/normalizer_test.go` | change (L51-65) | call the new names |
| `connector/codingagentconnector/internal/codextraces/normalizer_test.go` | change (L148-185) | call the new names |
| `connector/codingagentconnector/internal/genai/normalizer.go` | change (~L64-85) | defer through a `[]canonical.Claimer` |
| `connector/codingagentconnector/traces.go` | change (L24-30 comment) | name the `Claimer` values |
| `connector/codingagentconnector/config_structure_test.go` | change | Claimer-only guard, AST helpers |
| `connector/codingagentconnector/config_structure_helpers_test.go` | create | table tests for the AST helpers |
| `docs/design.md` | change (~L233-241) | describe the Claimer rule |

---

### Task 1: Add the `Claimer` interface and one implementation per harness

This task is additive. The old exported predicates stay in place until Task 2, so the tree builds after each task.

**Files:**
- Create: `connector/codingagentconnector/internal/canonical/claim.go`
- Change: `internal/claude/normalizer.go`, `internal/opencode/normalizer.go`, `internal/pi/normalizer.go`, `internal/openhands/normalizer.go`, `internal/codextraces/normalizer.go` (all under `connector/codingagentconnector/`)
- Test: `internal/<pkg>/claim_test.go`, one per package (create)

**Interfaces:**
- Produces: `canonical.Claimer` (interface: `Claim(ptrace.ResourceSpans) bool`). Produces `claude.Claimer`, `opencode.Claimer`, `pi.Claimer`, `openhands.Claimer`, and `codextraces.Claimer`, each of type `canonical.Claimer`.

- [ ] **Step 1: Write the failing tests**

Create `connector/codingagentconnector/internal/claude/claim_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName(claudeSpanPrefix + "interaction")
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("chat gpt-5")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
```

Create `internal/opencode/claim_test.go`. It uses the package's `scopeName` constant:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName(scopeName)
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName(scopeName + ".plugin")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
```

Create `internal/pi/claim_test.go`. It uses the package's `scopePrefix` constant:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName(scopePrefix)
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Scope().SetName("not-pi")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
```

Create `internal/openhands/claim_test.go`. `"conversation"` is a key of `markerSpanNames` in `internal/openhands/normalizer.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	claimed := ptrace.NewTraces()
	ss := claimed.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	ss.Spans().AppendEmpty().SetName("conversation")
	require.True(t, Claimer.Claim(claimed.ResourceSpans().At(0)))

	other := ptrace.NewTraces()
	unmarked := other.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty()
	unmarked.Scope().SetName(scopeName)
	unmarked.Spans().AppendEmpty().SetName("generic-laminar-span")
	require.False(t, Claimer.Claim(other.ResourceSpans().At(0)))
}
```

Create `internal/codextraces/claim_test.go`. It uses the test helpers `newCodexGroup`, `addTurn`, and `addSpan` from `normalizer_test.go`, the same fixtures as `TestContainsCodexSpansNeedsScopeAndMarker`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClaimerMatchesClaimPredicate(t *testing.T) {
	input := ptrace.NewTraces()
	addTurn(newCodexGroup(input))
	require.True(t, Claimer.Claim(input.ResourceSpans().At(0)))

	noMarker := ptrace.NewTraces()
	addSpan(newCodexGroup(noMarker), "run_turn", 1, 0, 0, time.Second)
	require.False(t, Claimer.Claim(noMarker.ResourceSpans().At(0)))
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `cd connector/codingagentconnector && go test ./internal/claude/ ./internal/opencode/ ./internal/pi/ ./internal/openhands/ ./internal/codextraces/ -run TestClaimerMatchesClaimPredicate`
Expected: FAIL to compile with `undefined: Claimer` in each package.

- [ ] **Step 3: Add the interface**

Create `connector/codingagentconnector/internal/canonical/claim.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

import "go.opentelemetry.io/collector/pdata/ptrace"

// Claimer reports whether a harness edge owns a resource group. Each trace
// harness package exports one as Claimer. An edge that must leave another
// edge's groups alone (the GenAI edge) calls that harness's Claimer, which is
// the only identifier one harness package may use from another
// (TestNoCrossHarnessImports).
type Claimer interface {
	Claim(ptrace.ResourceSpans) bool
}
```

- [ ] **Step 4: Add `claimer` and `Claimer` to each harness package**

In each package, add these lines directly below the existing exported predicate. In each, replace `<Predicate>` with that package's predicate: `ContainsClaudeSpans`, `ContainsOpenCodeSpans`, `ContainsPiSpans`, `ContainsOpenHandsSpans`, or `ContainsCodexSpans`. Every file already imports `canonical` and `ptrace`.

```go
// Claimer reports whether a resource group belongs to this edge.
var Claimer canonical.Claimer = claimer{}

type claimer struct{}

func (claimer) Claim(rs ptrace.ResourceSpans) bool { return <Predicate>(rs) }
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `cd connector/codingagentconnector && go test ./internal/claude/ ./internal/opencode/ ./internal/pi/ ./internal/openhands/ ./internal/codextraces/ ./internal/canonical/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/canonical/claim.go \
  connector/codingagentconnector/internal/{claude,opencode,pi,openhands,codextraces}/normalizer.go \
  connector/codingagentconnector/internal/{claude,opencode,pi,openhands,codextraces}/claim_test.go
git commit -m "refactor(connector): add a Claimer to each trace harness"
```

---

### Task 2: Defer through the Claimers and unexport the predicates

**Files:**
- Change: `connector/codingagentconnector/internal/genai/normalizer.go` (~L64-85)
- Change: the five harness `normalizer.go` files, to rename the predicate
- Change: `internal/opencode/normalizer_test.go:51-65` and `internal/codextraces/normalizer_test.go:148-185`
- Change: `connector/codingagentconnector/traces.go:24-30` (comment only)

**Interfaces:**
- Consumes: `canonical.Claimer`, and `claude.Claimer`, `opencode.Claimer`, `pi.Claimer`, `openhands.Claimer` from Task 1.
- Produces: unexported predicates `containsClaudeSpans`, `containsOpenCodeSpans`, `containsPiSpans`, `containsOpenHandsSpans`, and `containsCodexSpans`. After this task, genai references only `<pkg>.Claimer` from other harness packages, which Task 3's guard relies on.

The existing tests `TestGenAINormalizerSkipsUnknownScopesAndClaudeGroups`, `…SkipsOpenCodeGroups`, `…SkipsOpenHandsGroups`, and `…SkipsPiGroups` already pin the behavior of the genai deferral, so this task writes no new behavior test. Run them first to have a green baseline.

- [ ] **Step 1: Run the baseline**

Run: `cd connector/codingagentconnector && go test ./internal/genai/ -run 'Skips' -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)'`
Expected: four `--- PASS` lines, then `ok`.

- [ ] **Step 2: Rewrite the genai deferral**

In `internal/genai/normalizer.go`, add this package-level variable directly above `func (n *genAITraceNormalizer) ConsumeTraces`:

```go
// ownedElsewhere lists the edges that own a resource group before this one.
// Claude, OpenCode, Pi, and OpenHands groups can also carry GenAI scopes;
// claiming them here would emit each group twice. The Codex native edge claims
// scope==service.name groups, which no GenAI scope prefix can ever equal, so it
// is not listed.
var ownedElsewhere = []canonical.Claimer{
	claude.Claimer,
	opencode.Claimer,
	pi.Claimer,
	openhands.Claimer,
}

// claimedElsewhere reports whether another edge owns rs.
func claimedElsewhere(rs ptrace.ResourceSpans) bool {
	for _, c := range ownedElsewhere {
		if c.Claim(rs) {
			return true
		}
	}
	return false
}
```

In `ConsumeTraces`, replace the comment and the four `if <pkg>.Contains…Spans(inputResourceSpans) { continue }` blocks, and the two-line Codex comment that follows them, with:

```go
		if claimedElsewhere(inputResourceSpans) {
			continue
		}
```

Leave the `if !containsGenAIScopes(inputResourceSpans)` check that follows unchanged.

- [ ] **Step 3: Unexport the five predicates**

In each harness `normalizer.go`, rename the exported predicate to lower camel case, both its declaration and every in-package call, and update the first word of its doc comment. For example:

```
ContainsClaudeSpans    -> containsClaudeSpans
ContainsOpenCodeSpans  -> containsOpenCodeSpans
ContainsPiSpans        -> containsPiSpans
ContainsOpenHandsSpans -> containsOpenHandsSpans
ContainsCodexSpans     -> containsCodexSpans
```

Also change the `Claim` method body from Task 1 to call the new name. In `internal/claude/normalizer.go`, the doc comment says "The GenAI edge also calls this…"; change that sentence to "The GenAI edge reaches it through Claimer to leave Claude groups to the Claude normalizer."

- [ ] **Step 4: Update the two test files**

In `internal/opencode/normalizer_test.go`, rename `TestContainsOpenCodeSpansExactScopeOnly` to `TestClaimExactScopeOnly`, and change each `ContainsOpenCodeSpans(` call to `containsOpenCodeSpans(`.
In `internal/codextraces/normalizer_test.go`, rename `TestContainsCodexSpansNeedsScopeAndMarker` to `TestClaimNeedsScopeAndMarker`, and change each `ContainsCodexSpans(` call to `containsCodexSpans(`.
Do not change any assertion or message.

- [ ] **Step 5: Update the tracesRouter comment**

In `connector/codingagentconnector/traces.go`, replace:

```go
// normalizers. Each normalizer claims disjoint resource groups (the GenAI
// edge defers to Claude via claude.ContainsClaudeSpans, to OpenCode via
// opencode.ContainsOpenCodeSpans, and to Pi via pi.ContainsPiSpans; the
```

with:

```go
// normalizers. Each normalizer claims disjoint resource groups (the GenAI
// edge defers to Claude, OpenCode, Pi, and OpenHands through each package's
// Claimer; the
```

Then re-read the rest of the sentence and make sure it still parses.

- [ ] **Step 6: Run the tests and the name check**

Run: `cd connector/codingagentconnector && go vet ./... && go test ./...`
Expected: PASS. `TestNoCrossHarnessImports` still passes, because genai still imports the same four packages and the old exception table still lists them.

Run from the repository root: `rg -n 'Contains(Claude|OpenCode|Pi|OpenHands|Codex)Spans' --glob '!docs/superpowers/**'`
Expected: matches only in `config_structure_test.go` (the exception reasons, which Task 3 deletes) and `docs/design.md` (which Task 3 rewrites). No `.go` file outside `config_structure_test.go` matches.

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/genai/normalizer.go \
  connector/codingagentconnector/internal/{claude,opencode,pi,openhands,codextraces}/normalizer.go \
  connector/codingagentconnector/internal/opencode/normalizer_test.go \
  connector/codingagentconnector/internal/codextraces/normalizer_test.go \
  connector/codingagentconnector/traces.go
git commit -m "refactor(connector): defer GenAI groups through Claimers"
```

---

### Task 3: Allow only `Claimer` across harness packages

**Files:**
- Change: `connector/codingagentconnector/config_structure_test.go`: delete `crossHarnessImports` (L38-52) and rewrite `TestNoCrossHarnessImports` (~L67-110)
- Create: `connector/codingagentconnector/config_structure_helpers_test.go`
- Change: `docs/design.md` (~L233-241)

**Interfaces:**
- Consumes: Task 2's guarantee that genai uses only `<pkg>.Claimer`; Task 1's `var Claimer canonical.Claimer` in each harness. Consumes the existing helpers `harnessPackages`, `goFiles`, `sortedKeys`, `sharedPackages`, and `internalPrefix` in `config_structure_test.go`.
- Produces: `harnessReferences(dir string) (map[string]map[string]bool, error)`, `declaresClaimer(dir string) (bool, error)`, and `const claimerName = "Claimer"`.

- [ ] **Step 1: Write the failing helper tests**

Create `connector/codingagentconnector/config_structure_helpers_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeGo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600))
	return dir
}

func TestHarnessReferences(t *testing.T) {
	const claude = `"` + internalPrefix + `claude"`
	const canonical = `"` + internalPrefix + `canonical"`
	tests := []struct {
		name string
		src  string
		want map[string]map[string]bool
	}{
		{
			name: "claimer only",
			src:  "package p\nimport (\n" + claude + "\n" + canonical + "\n)\nvar _ = claude.Claimer\nvar _ = canonical.X\n",
			want: map[string]map[string]bool{"claude": {"Claimer": true}},
		},
		{
			name: "other identifier",
			src:  "package p\nimport " + claude + "\nvar _ = claude.ContainsClaudeSpans\n",
			want: map[string]map[string]bool{"claude": {"ContainsClaudeSpans": true}},
		},
		{
			name: "aliased import",
			src:  "package p\nimport c " + claude + "\nvar _ = c.Helper\n",
			want: map[string]map[string]bool{"claude": {"Helper": true}},
		},
		{
			name: "dot import",
			src:  "package p\nimport . " + claude + "\nvar _ = Claimer\n",
			want: map[string]map[string]bool{"claude": {".": true}},
		},
		{
			name: "blank import",
			src:  "package p\nimport _ " + claude + "\n",
			want: map[string]map[string]bool{"claude": {"_": true}},
		},
		{
			name: "import with no reference",
			src:  "package p\nimport " + claude + "\n",
			want: map[string]map[string]bool{"claude": {}},
		},
		{
			name: "shadowing local is not a reference",
			src:  "package p\nimport " + claude + "\nvar _ = claude.Claimer\nfunc f() { claude := struct{ X int }{}; _ = claude.X }\n",
			want: map[string]map[string]bool{"claude": {"Claimer": true}},
		},
		{
			name: "shared only",
			src:  "package p\nimport " + canonical + "\nvar _ = canonical.X\n",
			want: map[string]map[string]bool{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := harnessReferences(writeGo(t, tt.src))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDeclaresClaimer(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"typed var", "package p\nvar Claimer canonical.Claimer = claimer{}\n", true},
		{"typed var in group", "package p\nvar (\n\tx = 1\n\tClaimer canonical.Claimer = claimer{}\n)\n", true},
		{"untyped var", "package p\nvar Claimer = claimer{}\n", false},
		{"wrong type", "package p\nvar Claimer other.Claimer = claimer{}\n", false},
		{"func", "package p\nfunc Claimer() {}\n", false},
		{"absent", "package p\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := declaresClaimer(writeGo(t, tt.src))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `cd connector/codingagentconnector && go test -run 'TestHarnessReferences|TestDeclaresClaimer' .`
Expected: FAIL to compile with `undefined: harnessReferences` and `undefined: declaresClaimer`.

- [ ] **Step 3: Add the helpers**

In `config_structure_test.go`, add `"go/ast"` to the imports and append these declarations after `internalImports`:

```go
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
```

`harnessReferences` scans test files too, like `internalImports` does today. A harness package's tests may not reach into another harness either.

- [ ] **Step 4: Run the helper tests and confirm they pass**

Run: `cd connector/codingagentconnector && go test -run 'TestHarnessReferences|TestDeclaresClaimer' -v . 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: every case shows `--- PASS`, then `ok`.

If the "shadowing local" case fails because `ident.Obj` is nil for the local `claude`, the parser mode is missing object resolution. Mode `0` resolves objects, so confirm that `parser.SkipObjectResolution` is not set.

- [ ] **Step 5: Replace the exception table and rewrite the guard**

In `config_structure_test.go`, delete the `crossHarnessImports` comment and variable (the block that starts `// crossHarnessImports records every place…` and ends with the map's closing `}`). Replace the whole `TestNoCrossHarnessImports` function with:

```go
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
```

Now `internalImports` may have no caller. Check with `rg -n 'internalImports\(' connector/codingagentconnector`. If the only match is its own definition, delete it, because golangci-lint's `unused` check would flag it.

- [ ] **Step 6: Prove the guard rejects a real violation**

Temporarily add `var _ = claude.NewDefaultConfig` to `internal/genai/normalizer.go`: an exported `internal/claude` function other than `Claimer`.
Run: `cd connector/codingagentconnector && go test -run TestNoCrossHarnessImports .`
Expected: FAIL with `internal/genai uses claude.NewDefaultConfig from harness package internal/claude.`
Remove the temporary line and run the test again. Expected: PASS.

- [ ] **Step 7: Rewrite the design rule**

In `docs/design.md`, replace the bullet that begins `- **Harness packages never import another harness package.**` (through `…any other cross-harness import fails.`) with:

```markdown
- **Harness packages use nothing from another harness package except its
  `Claimer`.** Shared helpers and constants go in `internal/canonical` or
  `internal/content`. Each trace harness exports `var Claimer
  canonical.Claimer`, which reports whether that edge owns a resource group.
  An edge that defers to other edges calls their `Claimer` values: the generic
  GenAI edge leaves Claude, OpenCode, Pi, and OpenHands groups to their own
  edges this way. The guard test rejects any other identifier, a dot or blank
  import of a harness package, and a harness package that is imported without
  declaring a `Claimer`.
```

- [ ] **Step 8: Run all the gates**

Run from the repository root:

```bash
rg -n 'Contains(Claude|OpenCode|Pi|OpenHands|Codex)Spans|crossHarnessImports' --glob '!docs/superpowers/**'
```

Expected: matches only in `config_structure_helpers_test.go`, inside the "other identifier" case's source string. No other matches.

Run: `./scripts/check.sh`
Expected: the script exits 0. If a part of it cannot run locally (Docker image builds, for example), record which part and why, and run at least `gofmt -l .` (empty output), `cd connector/codingagentconnector && go vet ./... && go test ./... && go test -race ./...`, and `golangci-lint run ./...` in `connector/codingagentconnector`.

- [ ] **Step 9: Commit**

```bash
git add connector/codingagentconnector/config_structure_test.go \
  connector/codingagentconnector/config_structure_helpers_test.go \
  docs/design.md
git commit -m "test(connector): allow only Claimer across harness packages"
```
