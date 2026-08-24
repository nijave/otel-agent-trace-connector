# GitHub Copilot native-trace support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Canonicalize GitHub Copilot CLI and VS Code Chat native OTel traces by claiming instrumentation scope `github.copilot` inside the existing GenAI-semconv normalizer.

**Architecture:** One scope entry added to the GenAI edge's claim list — the shared normalizer already performs every rename, content strip, and marker stamp Copilot output needs. A committed OTLP fixture built from the source-verified wire schema guards against upstream drift without a live E2E stack.

**Tech Stack:** Go, OpenTelemetry Collector pdata, testify.

**Spec:** `docs/superpowers/specs/2026-08-22-copilot-traces-design.md`

## Global Constraints

- Route inside `internal/genai`; no new package, no new config surface (spec decision).
- No configurable scope allowlist this round; custom `COPILOT_OTEL_SOURCE_NAME` values do not claim (spec decision, documented limitation).
- Vendor attributes pass through untouched; do not invent canonical vocabulary for `github.copilot.cost`, `.aiu`, `.turn_id`, `.interaction_id`, `.turn_count` (spec decision).
- No production code beyond the one scope-list entry; all behavior comes from the shared normalizer.
- All Go commands run with `connector/codingagentconnector/` as working directory.
- The repo requires pull requests; direct pushes to `main` are rejected. Commit atomic units on a branch.
- Leave any pre-existing unrelated working-tree changes (e.g. root `go.mod`/`go.sum`) unstaged; stage explicit paths only.

---

### Task 1: Claim the `github.copilot` scope

**Files:**
- Modify: `connector/codingagentconnector/internal/genai/normalizer.go:31-36`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`

**Interfaces:**
- Consumes: existing `New(next consumer.Traces) connector.Traces`; test helpers `newGroup(traces ptrace.Traces, scopeName, spanName string) ptrace.Span`, `traceSink`.
- Produces: the GenAI edge claims resource groups whose instrumentation scope starts with `github.copilot`. No exported API changes.

- [ ] **Step 1: Write the failing tests**

Add to `normalizer_test.go`. The import block gains `"go.opentelemetry.io/collector/pdata/pcommon"`.

```go
func TestGenAINormalizerClaimsCopilotScope(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "github-copilot")
	rs.Resource().Attributes().PutStr("service.version", "1.0.64")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("github.copilot")

	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent")
	root.SetKind(ptrace.SpanKindInternal)
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.agent.name", "copilot-cli")
	root.Attributes().PutStr("gen_ai.conversation.id", "11111111-2222-3333-4444-555555555555")
	root.Attributes().PutInt("gen_ai.usage.input_tokens", 120)
	root.Attributes().PutInt("gen_ai.usage.output_tokens", 80)
	root.Attributes().PutDouble("github.copilot.cost", 0.15)
	root.Attributes().PutInt("github.copilot.aiu", 1)
	// Capture-gated content must never survive on claimed spans.
	root.Attributes().PutStr("gen_ai.system_instructions", "secret system prompt")

	chat := ss.Spans().AppendEmpty()
	chat.SetName("chat")
	chat.SetKind(ptrace.SpanKindClient)
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "gpt-5.2")

	hook := ss.Spans().AppendEmpty()
	hook.SetName("execute_hook PreToolUse")
	hook.Attributes().PutStr("gen_ai.operation.name", "execute_hook")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "invoke_agent copilot-cli", spans.At(0).Name())
	require.Equal(t, "chat gpt-5.2", spans.At(1).Name())
	// Operations outside the rename table keep their wire names.
	require.Equal(t, "execute_hook PreToolUse", spans.At(2).Name())

	attrs := spans.At(0).Attributes()
	require.Equal(t, "native", fixtureAttrString(t, attrs, "coding_agent.source"))
	require.Equal(t, "github.copilot", fixtureAttrString(t, attrs, "coding_agent.source.scope"))
	require.Equal(t, "github-copilot", fixtureAttrString(t, attrs, "coding_agent.client.name"))
	require.Equal(t, "1.0.64", fixtureAttrString(t, attrs, "coding_agent.client.version"))
	cost, ok := attrs.Get("github.copilot.cost")
	require.True(t, ok, "vendor extras pass through untouched")
	require.Equal(t, 0.15, cost.Double())
	aiu, ok := attrs.Get("github.copilot.aiu")
	require.True(t, ok)
	require.Equal(t, int64(1), aiu.Int())
	_, ok = attrs.Get("gen_ai.system_instructions")
	require.False(t, ok, "capture-gated content must be stripped")

	// The input handed to the connector is never mutated.
	_, ok = input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes().Get("gen_ai.system_instructions")
	require.True(t, ok)
}

func fixtureAttrString(t *testing.T, attrs pcommon.Map, key string) string {
	t.Helper()
	value, ok := attrs.Get(key)
	require.True(t, ok, "attribute %q missing", key)
	return value.Str()
}
```

Extend the scope list inside `TestGenAINormalizerClaimsKnownScopes` with one entry:

```go
		"github.copilot",
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/genai/ -run 'TestGenAINormalizerClaims' -v`
Expected: FAIL — `TestGenAINormalizerClaimsCopilotScope` gets an empty sink (length assertion fails); `TestGenAINormalizerClaimsKnownScopes` fails on the `github.copilot` case.

- [ ] **Step 3: Add the scope entry**

In `normalizer.go`, extend `scopePrefixes`:

```go
var scopePrefixes = []string{
	"opentelemetry.instrumentation.openai_v2",
	"opentelemetry.util.genai",
	"opentelemetry.instrumentation.genai",
	"strands.telemetry",
	// GitHub Copilot CLI / VS Code Chat; prefix form tolerates sub-scopes.
	"github.copilot",
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/genai/ -v`
Expected: PASS — the whole suite, including Claude/OpenCode disjointness tests.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/genai/normalizer.go connector/codingagentconnector/internal/genai/normalizer_test.go
git commit -m "genai: claim GitHub Copilot native traces"
```

### Task 2: Committed fixture replay from the documented schema

**Files:**
- Create: `connector/codingagentconnector/internal/genai/testdata/copilot-native.otlp.json`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`

**Interfaces:**
- Consumes: `loadFixtureLines(t, path) ptrace.Traces` (JSON-lines: each line one full OTLP JSON traces document), `eachFixtureSpan(t, traces, visit)`; the claiming behavior from Task 1; helper `fixtureAttrString` from Task 1.
- Produces: nothing exported; the fixture pins the documented Copilot wire shape against upstream drift.

- [ ] **Step 1: Write the failing test**

Add to `normalizer_test.go`:

```go
func TestGenAINormalizerProcessesCapturedCopilotFixture(t *testing.T) {
	input := loadFixtureLines(t, filepath.Join("testdata", "copilot-native.otlp.json"))
	require.NotZero(t, input.SpanCount())

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outputs := sink.all()
	require.Len(t, outputs, 1, "both fixture batches share the claimed scope")
	require.Equal(t, 2, outputs[0].ResourceSpans().Len(), "both flavors stay distinct resource groups")

	names := map[string]ptrace.Span{}
	eachFixtureSpan(t, outputs[0], func(span ptrace.Span) {
		for _, key := range contentAttributeKeys {
			_, exists := span.Attributes().Get(key)
			require.False(t, exists, "content attribute %q survived on %q", key, span.Name())
		}
		names[span.Name()] = span
	})

	cliRoot, ok := names["invoke_agent copilot-cli"]
	require.True(t, ok, "CLI invoke_agent root renames by agent name")
	require.Equal(t, "native", fixtureAttrString(t, cliRoot.Attributes(), "coding_agent.source"))
	require.Equal(t, "github.copilot", fixtureAttrString(t, cliRoot.Attributes(), "coding_agent.source.scope"))
	require.Equal(t, "github-copilot", fixtureAttrString(t, cliRoot.Attributes(), "coding_agent.client.name"))
	require.Equal(t, "11111111-2222-3333-4444-555555555555", fixtureAttrString(t, cliRoot.Attributes(), "gen_ai.conversation.id"))
	cost, ok := cliRoot.Attributes().Get("github.copilot.cost")
	require.True(t, ok, "vendor cost passes through")
	require.Equal(t, 0.15, cost.Double())
	var shutdownSeen bool
	for i := 0; i < cliRoot.Events().Len(); i++ {
		if cliRoot.Events().At(i).Name() == "github.copilot.session.shutdown" {
			shutdownSeen = true
		}
	}
	require.True(t, shutdownSeen, "lifecycle span events survive")

	chat, ok := names["chat gpt-5.2"]
	require.True(t, ok)
	require.Equal(t, "t-1", fixtureAttrString(t, chat.Attributes(), "github.copilot.turn_id"))

	tool, ok := names["execute_tool run_commands"]
	require.True(t, ok)
	require.Equal(t, "function", fixtureAttrString(t, tool.Attributes(), "gen_ai.tool.type"))

	vsCodeRoot, ok := names["invoke_agent copilotcli"]
	require.True(t, ok, "VS Code flavor renames by its own agent name")
	reasoning, ok := vsCodeRoot.Attributes().Get("gen_ai.usage.reasoning.output_tokens")
	require.True(t, ok, "the reasoning-token key passes through unmapped")
	require.Equal(t, int64(25), reasoning.Int())
	require.Equal(t, "https://github.com/acme/widgets", fixtureAttrString(t, vsCodeRoot.Attributes(), "github.copilot.git.repository"))
	require.Equal(t, "https://github.com/acme/widgets", fixtureAttrString(t, vsCodeRoot.Attributes(), "copilot_chat.repo.remote_url"), "legacy namespace rides along untouched")

	hook, ok := names["execute_hook PreToolUse"]
	require.True(t, ok, "unknown operations keep their wire names")
	require.Equal(t, "pass", fixtureAttrString(t, hook.Attributes(), "github.copilot.hook.decision"))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/genai/ -run TestGenAINormalizerProcessesCapturedCopilotFixture -v`
Expected: FAIL — the fixture file does not exist yet (`loadFixtureLines` errors).

- [ ] **Step 3: Create the fixture**

Create `connector/codingagentconnector/internal/genai/testdata/copilot-native.otlp.json`. The file is JSON-lines: exactly two physical lines, no pretty printing. Line 1 is the CLI flavor:

```json
{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"github-copilot"}},{"key":"service.version","value":{"stringValue":"1.0.64"}}]},"scopeSpans":[{"scope":{"name":"github.copilot"},"spans":[{"traceId":"5b8aa5a2d2c872e987626f4d7f1a1e2c","spanId":"a1b2c3d4e5f60718","name":"invoke_agent","kind":1,"startTimeUnixNano":"1787300000000000000","endTimeUnixNano":"1787300010000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}},{"key":"gen_ai.provider.name","value":{"stringValue":"github"}},{"key":"gen_ai.agent.id","value":{"stringValue":"github.copilot.default"}},{"key":"gen_ai.agent.name","value":{"stringValue":"copilot-cli"}},{"key":"gen_ai.conversation.id","value":{"stringValue":"11111111-2222-3333-4444-555555555555"}},{"key":"enduser.pseudo.id","value":{"stringValue":"user-42"}},{"key":"gen_ai.request.model","value":{"stringValue":"gpt-5.2"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"120"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":"80"}},{"key":"gen_ai.usage.cache_read.input_tokens","value":{"intValue":"40"}},{"key":"gen_ai.usage.cache_creation.input_tokens","value":{"intValue":"10"}},{"key":"github.copilot.turn_count","value":{"intValue":"2"}},{"key":"github.copilot.cost","value":{"doubleValue":0.15}},{"key":"github.copilot.aiu","value":{"intValue":"1"}},{"key":"gen_ai.system_instructions","value":{"stringValue":"secret system prompt"}}],"events":[{"timeUnixNano":"1787300009000000000","name":"github.copilot.session.shutdown","attributes":[{"key":"github.copilot.total_premium_requests","value":{"intValue":"1"}}]}]},{"traceId":"5b8aa5a2d2c872e987626f4d7f1a1e2c","spanId":"b2c3d4e5f6071829","parentSpanId":"a1b2c3d4e5f60718","name":"chat","kind":3,"startTimeUnixNano":"1787300001000000000","endTimeUnixNano":"1787300005000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},{"key":"gen_ai.request.model","value":{"stringValue":"gpt-5.2"}},{"key":"gen_ai.response.model","value":{"stringValue":"gpt-5.2"}},{"key":"gen_ai.conversation.id","value":{"stringValue":"11111111-2222-3333-4444-555555555555"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"60"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":"30"}},{"key":"github.copilot.turn_id","value":{"stringValue":"t-1"}},{"key":"github.copilot.interaction_id","value":{"stringValue":"i-1"}},{"key":"gen_ai.response.time_to_first_chunk","value":{"doubleValue":0.42}}]},{"traceId":"5b8aa5a2d2c872e987626f4d7f1a1e2c","spanId":"c3d4e5f60718293a","parentSpanId":"a1b2c3d4e5f60718","name":"execute_tool","kind":1,"startTimeUnixNano":"1787300006000000000","endTimeUnixNano":"1787300007000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"execute_tool"}},{"key":"gen_ai.tool.name","value":{"stringValue":"run_commands"}},{"key":"gen_ai.tool.type","value":{"stringValue":"function"}},{"key":"gen_ai.tool.call.id","value":{"stringValue":"call-9"}},{"key":"gen_ai.tool.call.arguments","value":{"stringValue":"{\"cmd\":\"ls\"}"}}]}]}]}
```

Line 2 is the VS Code Chat flavor:

```json
{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"copilot-chat"}},{"key":"session.id","value":{"stringValue":"vscode-window-7"}}]},"scopeSpans":[{"scope":{"name":"github.copilot"},"spans":[{"traceId":"6c9bb6b3e3d9830f98737050802b2f3d","spanId":"d4e5f60718293a4b","name":"invoke_agent","kind":1,"startTimeUnixNano":"1787300100000000000","endTimeUnixNano":"1787300110000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}},{"key":"gen_ai.agent.name","value":{"stringValue":"copilotcli"}},{"key":"gen_ai.conversation.id","value":{"stringValue":"22222222-3333-4444-5555-666666666666"}},{"key":"gen_ai.usage.reasoning.output_tokens","value":{"intValue":"25"}},{"key":"github.copilot.git.repository","value":{"stringValue":"https://github.com/acme/widgets"}},{"key":"github.copilot.git.branch","value":{"stringValue":"main"}},{"key":"github.copilot.git.commit_sha","value":{"stringValue":"abc123def4567890"}},{"key":"copilot_chat.repo.remote_url","value":{"stringValue":"https://github.com/acme/widgets"}}]},{"traceId":"6c9bb6b3e3d9830f98737050802b2f3d","spanId":"e5f60718293a4b5c","parentSpanId":"d4e5f60718293a4b","name":"execute_hook PreToolUse","kind":1,"startTimeUnixNano":"1787300101000000000","endTimeUnixNano":"1787300102000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"execute_hook"}},{"key":"github.copilot.hook.decision","value":{"stringValue":"pass"}}]}]}]}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/genai/ -run TestGenAINormalizerProcessesCapturedCopilotFixture -v`
Expected: PASS.

- [ ] **Step 5: Run the whole module suite**

Run: `go test ./...`
Expected: PASS everywhere.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/genai/testdata/copilot-native.otlp.json connector/codingagentconnector/internal/genai/normalizer_test.go
git commit -m "genai: pin Copilot wire shape with committed fixture"
```

### Task 3: Docs updates and PR

**Files:**
- Modify: `README.md` (repo root)
- Modify: `connector/codingagentconnector/README.md`
- Modify: `docs/design.md`
- Modify: `docs/harnesses.md`

**Interfaces:**
- Consumes: implemented behavior from Tasks 1–2.
- Produces: documentation matching the shipped behavior.

- [ ] **Step 1: Root README — harness bullet and table row**

In the top bullets (after the OpenCode bullet), add:

```markdown
- **GitHub Copilot:** normalizes native GenAI-semconv traces from Copilot CLI
  and VS Code Chat (`invoke_agent`/`chat`/`execute_tool`) through the shared
  GenAI-semconv edge
```

In the "Supported harnesses" table (after the OpenCode row), add:

```markdown
| GitHub Copilot | traces | instrumentation scope starting with `github.copilot` (GenAI-semconv edge) | `COPILOT_OTEL_ENABLED=true` or set `OTEL_EXPORTER_OTLP_ENDPOINT`; OTLP/HTTP or file exporter |
```

- [ ] **Step 2: Connector README — harness bullet**

After the OpenCode bullet in `connector/codingagentconnector/README.md`, add:

```markdown
- **GitHub Copilot (traces → traces):** claims instrumentation scope
  `github.copilot` through the shared GenAI-semconv edge; spans rename via
  the standard operation table, capture-gated content never reaches output,
  and vendor extras (`github.copilot.cost`, `.aiu`, `.turn_id`) pass through
  untouched.
```

- [ ] **Step 3: design.md — implemented sources and future work**

In `docs/design.md`, extend the "Implemented sources" list with one line after the OpenCode entry:

```markdown
  - GitHub Copilot native-span normalization (via the GenAI edge, scope
    prefix `github.copilot`).
```

Replace the future-work bullet that reads "Add GitHub Copilot support: the same provider edge + live E2E for Copilot, likewise pending confirmation of its telemetry format." with:

```markdown
- Copilot native traces are handled via the GenAI edge. A live Copilot E2E
  stack is deferred until someone with a paid subscription validates
  non-interactive CLI invocation; committed fixtures cover the documented
  schema meanwhile. A renamed producer scope (`COPILOT_OTEL_SOURCE_NAME`)
  does not claim.
```

- [ ] **Step 4: harnesses.md — relevance section**

In the "Relevance to the connector" section of `docs/harnesses.md`, directly after the paragraph beginning "The native OpenCode path is now handled", add:

```markdown
GitHub Copilot native traces are handled the same way: the GenAI edge claims
instrumentation scope `github.copilot` (CLI GA since v1.0.4; VS Code Chat
speaks the identical vocabulary). The cloud coding agent has no OTLP export,
so its hooks path stays out of scope; the JetBrains extension shows no
telemetry at all. Research record: verified against primary sources on
2026-08-22.
```

- [ ] **Step 5: Verify docs render sensibly**

Run: `git diff --stat` then skim each touched doc section.
Expected: only the intended additions; no broken tables (pipe counts unchanged per row).

- [ ] **Step 6: Run the full pre-push check**

Run from the repo root: `./scripts/check.sh`
Expected: green end to end (this is the repo's push gate).

- [ ] **Step 7: Commit, push branch, open PR**

```bash
git checkout -b copilot-native-traces
git add README.md connector/codingagentconnector/README.md docs/design.md docs/harnesses.md
git commit -m "docs: GitHub Copilot joins the supported harnesses"
git push -u origin copilot-native-traces
gh pr create --title "Copilot native-trace support via the GenAI edge" --body "Implements docs/superpowers/specs/2026-08-22-copilot-traces-design.md"
```

(If Tasks 1–2 were committed on this same branch, the PR carries all three commits; keep the commit boundaries from each task.)
