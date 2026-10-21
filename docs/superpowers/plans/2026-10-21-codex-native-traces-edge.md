# Codex Native Traces Edge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a stateless traces-to-traces edge that turns Codex's native `tracing` spans (exported with `otel.trace_exporter`) into `invoke_agent codex` / `chat <model>` / `execute_tool <tool>` canonical spans, and map `call_id` on the logs edge's tool spans.

**Architecture:** A new package `internal/codextraces` mirrors the Pi edge: claim a resource group by scope name and marker span names, rebuild each kept span's attributes from a fresh map, drop everything else, reparent kept children whose parent left the batch onto the turn root. The e2e Codex stack turns the trace exporter on so one live run produces the fixture and keeps exercising both edges; the `signals` switch (plan 1) is how an operator avoids consuming both in production.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`/`pcommon`), testify, jq for fixture slicing, the Codex e2e stack (`scripts/e2e.sh`, paid).

**Spec:** `docs/superpowers/specs/2026-10-19-harness-feature-gaps-design.md`, section 5 and the Background facts about Codex spans. Read it first.

## Global Constraints

- Work from `connector/codingagentconnector/` for Go commands; prefix every `go` command with `GOTOOLCHAIN=go1.27.0`.
- `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh` from the repo root must end `ALL CHECKS PASSED` before pushing.
- Every kept span rebuilds its attribute map from scratch. No tracing-opentelemetry bookkeeping (`code.*`, `thread.*`, `busy_ns`, `idle_ns`, `target`, `level`) and no `codex.*` key may reach output.
- The edge keeps Codex's trace ids and span ids. It never synthesizes ids and never merges turns into one trace.
- `gen_ai.provider.name` = `openai` on the root and chat spans; `coding_agent.source` = `native`; `coding_agent.client.name` = `codex`; `coding_agent.source.event` = the wire span name.
- The logs edge (`internal/codex`) changes only in `appendToolSpans` (`call_id` → `gen_ai.tool.call.id`).
- Stage explicit paths only. Fixture values stay synthetic where they are identity (the e2e run uses API-key auth, so the capture carries none).
- The live Codex run (Task 1 and Task 9) costs money; run it exactly as written and do not loop it.

## Review Focus

1. **A batch of chat and tool spans whose turn span has not ended yet must not lose those spans.** The turn closes last, so Codex exports children first. Task 5 tests a turn-less batch: children keep their wire parent id and still emit.
2. **A chat span must get its model from the turn in the same batch and stay bare `chat` when no turn is present.** Task 3 tests both.
3. **The usage-bearing span's name is whatever the response event type was; the edge must classify by the `gen_ai.usage.input_tokens` attribute, not the name.** Task 3 tests two different wire names.
4. **A `codex.tool_result` event whose `success` is the string `"false"` must still mark the span as failed.** tracing fields may arrive as strings. Task 4 tests string and bool forms.
5. **Running both edges yields two canonical roots per turn and the connector must not try to dedupe them.** Task 8's docs state it; Task 9's validator asserts both roots exist in the live run so the behavior is visible, not hidden.

---

### Task 1: Capture Codex native traces from the e2e stack

**Files:**
- Change: `e2e/codex/config.toml`
- Create: `connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json`

**Interfaces:**
- Produces: the committed raw fixture and a verified list of wire span names and attribute keys that Tasks 2 through 5 use.

- [ ] **Step 1: Turn the trace exporter on**

In `e2e/codex/config.toml`, extend the `[otel]` block:

```toml
[otel]
environment = "e2e"
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://collector:4318/v1/logs", protocol = "binary" } }
# Native spans ride the shared traces/native pipeline so the Codex traces edge
# is exercised beside the logs edge; the validator asserts both roots.
trace_exporter = { otlp-http = { endpoint = "http://collector:4318/v1/traces", protocol = "binary" } }
```

The key and shape come from `codex-rs/config/src/types.rs` (`OtelConfigToml.trace_exporter: Option<OtelExporterKind>`, tag `rust-v0.163.0`); it takes the same value as `exporter`.

- [ ] **Step 2: Run the live Codex stack once**

Export the OpenCode Go key from the keyring per `e2e/README.md` (never echo it), then from the repo root:

```bash
GOTOOLCHAIN=go1.27.0 ./scripts/e2e.sh
```

Expected: the validator passes on the logs edge's trace as before, and `.e2e-output/raw-traces.json` is non-empty. Save the run id: the script prints `E2E_RUN_ID` (or read it from `.e2e-output/canonical-traces.json`'s `e2e.run.id`).

- [ ] **Step 3: Record what the wire carries**

Run from the repo root (replace `RUN` with the run id):

```bash
python3 -I - RUN <<'EOF'
import json, sys, collections
run = sys.argv[1]
names = collections.Counter(); keys = collections.defaultdict(set); events = collections.Counter(); scopes = set()
for line in open('.e2e-output/raw-traces.json'):
    if not line.strip(): continue
    for rs in json.loads(line)['resourceSpans']:
        res = {a['key']: a['value'] for a in rs['resource']['attributes']}
        if res.get('e2e.run.id', {}).get('stringValue') != run: continue
        svc = res.get('service.name', {}).get('stringValue')
        for ss in rs['scopeSpans']:
            scopes.add((ss['scope'].get('name'), svc))
            for s in ss['spans']:
                names[s['name']] += 1
                for a in s.get('attributes', []): keys[s['name']].add(a['key'])
                for e in s.get('events', []):
                    ename = e.get('name'); eattr = {a['key']: a['value'] for a in e.get('attributes', [])}
                    events[(ename, eattr.get('event.name', {}).get('stringValue'))] += 1
print('scopes (scope name, service.name):', scopes)
for n, c in names.most_common(): print(c, n, sorted(keys[n]))
print('events (span event name, event.name attr):'); [print(' ', c, e) for e, c in events.most_common()]
EOF
```

Compare against the spec's Background facts and write the findings into the PR description. The things later tasks depend on:

- the scope name equals the resource `service.name` (`codex_exec`);
- the turn span carries the name `session_task.turn` and `thread.id`, `turn.id`, `model`, `codex.turn.reasoning_effort`, and the `codex.turn.token_usage.*` keys;
- exactly one span per model call carries `gen_ai.usage.input_tokens` (note its name; the plan treats the name as opaque);
- the `codex.tool_result` event appears as a span event: note whether its name is `codex.tool_result` or whether the name is only in the event's `event.name` attribute, and which span carries it;
- `mcp.tools.call` spans are absent (the e2e has no MCP server); the unit tests in Task 4 cover them from the source-derived shape.

If a name differs from the spec, use the wire's name in the constants of Tasks 2 through 4 and say so in the PR.

- [ ] **Step 4: Slice the fixture**

Keep the run's resource groups whose scope matches the service name, and within them every span that the edge keeps plus up to 20 other spans so the fixture exercises dropping. Run from the repo root (replace `RUN`):

```bash
mkdir -p connector/codingagentconnector/internal/codextraces/testdata
jq -c --arg run RUN '
  def kept: (.name | test("^session_task\\.|^mcp\\.tools\\.call$"))
    or any(.attributes[]?; .key == "gen_ai.usage.input_tokens")
    or any(.events[]?; .name == "codex.tool_result" or any(.attributes[]?; .key == "event.name" and .value.stringValue == "codex.tool_result"));
  .resourceSpans |= map(
    select(any(.resource.attributes[]?; .key == "e2e.run.id" and .value.stringValue == $run))
    | ([.resource.attributes[]? | select(.key == "service.name") | .value.stringValue][0]) as $svc
    | .scopeSpans |= map(select(.scope.name == $svc)
        | .spans |= ((map(select(kept))) + ([.[] | select(kept | not)][:20])))
    | .scopeSpans |= map(select(.spans | length > 0)))
  | select(.resourceSpans | length > 0)' \
  .e2e-output/raw-traces.json > connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json
```

Then confirm the file holds at least one `session_task.turn`, one span with `gen_ai.usage.input_tokens`, and at least one `codex.tool_result` event:

```bash
rg -c '"session_task.turn"' connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json
rg -c '"gen_ai.usage.input_tokens"' connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json
rg -c 'codex.tool_result' connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json
```

Each must be at least 1. The file is JSONL (one export per line); the edge's tests load it with the same merge the Claude edge uses.

- [ ] **Step 5: Commit**

```bash
git add e2e/codex/config.toml connector/codingagentconnector/internal/codextraces/testdata/codex-native-traces.json
git commit -m "test(codex): capture native traces from the e2e stack"
```

### Task 2: Package skeleton, claiming, and the turn root

**Files:**
- Create: `connector/codingagentconnector/internal/codextraces/normalizer.go`
- Create: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`

**Interfaces:**
- Produces: `codextraces.New(next consumer.Traces, captureIdentity bool) connector.Traces`, `codextraces.ContainsCodexSpans(rs ptrace.ResourceSpans) bool`, package-private `normalizeSpan(span ptrace.Span, version, model string) (kind spanKind)` where `spanKind` is `kindDrop | kindRoot | kindChat | kindTool`.

- [ ] **Step 1: Write the failing tests**

Create `normalizer_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type traceSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return nil
}
func (s *traceSink) all() []ptrace.Traces {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ptrace.Traces(nil), s.traces...)
}

var (
	traceOne = pcommon.TraceID([16]byte{1})
	baseTime = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
)

// newCodexGroup builds one resource group the way codex_exec exports it: the
// scope name equals the resource service.name.
func newCodexGroup(input ptrace.Traces) ptrace.SpanSlice {
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex_exec")
	rs.Resource().Attributes().PutStr("service.version", "0.163.0")
	rs.Resource().Attributes().PutStr("env", "e2e")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("codex_exec")
	return ss.Spans()
}

func addSpan(spans ptrace.SpanSlice, name string, id byte, parent byte, start, end time.Duration) ptrace.Span {
	span := spans.AppendEmpty()
	span.SetName(name)
	span.SetTraceID(traceOne)
	span.SetSpanID(pcommon.SpanID([8]byte{id}))
	if parent != 0 {
		span.SetParentSpanID(pcommon.SpanID([8]byte{parent}))
	}
	span.SetKind(ptrace.SpanKindInternal)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(start)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(end)))
	// tracing-opentelemetry bookkeeping every exported span carries.
	span.Attributes().PutStr("code.file.path", "core/src/tasks/mod.rs")
	span.Attributes().PutInt("code.line.number", 355)
	span.Attributes().PutInt("busy_ns", 12)
	span.Attributes().PutInt("idle_ns", 34)
	span.Attributes().PutStr("thread.name", "tokio-runtime-worker")
	return span
}

func addTurn(spans ptrace.SpanSlice) ptrace.Span {
	turn := addSpan(spans, "session_task.turn", 1, 0, 0, 10*time.Second)
	turn.Attributes().PutStr("thread.id", "thread-0001")
	turn.Attributes().PutStr("turn.id", "turn-0001")
	turn.Attributes().PutStr("model", "gpt-5.6-luna")
	turn.Attributes().PutStr("codex.turn.reasoning_effort", "low")
	turn.Attributes().PutInt("codex.turn.token_usage.input_tokens", 100)
	turn.Attributes().PutInt("codex.turn.token_usage.cached_input_tokens", 40)
	turn.Attributes().PutInt("codex.turn.token_usage.cache_write_input_tokens", 7)
	turn.Attributes().PutInt("codex.turn.token_usage.non_cached_input_tokens", 60)
	turn.Attributes().PutInt("codex.turn.token_usage.output_tokens", 20)
	turn.Attributes().PutInt("codex.turn.token_usage.reasoning_output_tokens", 5)
	turn.Attributes().PutInt("codex.turn.token_usage.total_tokens", 120)
	return turn
}

func normalizeOne(t *testing.T, input ptrace.Traces) ptrace.SpanSlice {
	t.Helper()
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	all := ptrace.NewSpanSlice()
	for _, traces := range sink.all() {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					spans.At(k).CopyTo(all.AppendEmpty())
				}
			}
		}
	}
	return all
}

func findSpan(t *testing.T, spans ptrace.SpanSlice, name string) ptrace.Span {
	t.Helper()
	for i := 0; i < spans.Len(); i++ {
		if spans.At(i).Name() == name {
			return spans.At(i)
		}
	}
	t.Fatalf("span %q not found", name)
	return ptrace.Span{}
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on %s", key, span.Name())
	return value.Str()
}

func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on %s", key, span.Name())
	return value.Int()
}

func requireOnlyCanonical(t *testing.T, span ptrace.Span) {
	t.Helper()
	span.Attributes().Range(func(key string, _ pcommon.Value) bool {
		require.NotContains(t, key, "codex.", "raw key %s on %s", key, span.Name())
		require.NotContains(t, key, "code.", "bookkeeping key %s on %s", key, span.Name())
		require.NotContains(t, key, "thread.", "bookkeeping key %s on %s", key, span.Name())
		require.NotEqual(t, "busy_ns", key)
		require.NotEqual(t, "idle_ns", key)
		require.NotEqual(t, "model", key)
		return true
	})
}

func TestContainsCodexSpansNeedsScopeAndMarker(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	require.True(t, ContainsCodexSpans(input.ResourceSpans().At(0)))

	noMarker := ptrace.NewTraces()
	addSpan(newCodexGroup(noMarker), "run_turn", 1, 0, 0, time.Second)
	require.False(t, ContainsCodexSpans(noMarker.ResourceSpans().At(0)), "bookkeeping spans alone do not claim")

	wrongScope := ptrace.NewTraces()
	rs := wrongScope.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "something-else")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("codex_exec")
	addTurn(ss.Spans())
	require.False(t, ContainsCodexSpans(wrongScope.ResourceSpans().At(0)), "the scope must equal the service name")

	dispatch := ptrace.NewTraces()
	addSpan(newCodexGroup(dispatch), "op.dispatch.user_turn", 1, 0, 0, time.Second)
	require.True(t, ContainsCodexSpans(dispatch.ResourceSpans().At(0)), "op.dispatch.* marks a Codex group too")
}

func TestTurnBecomesInvokeAgentRoot(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	turn := addTurn(spans)
	turn.SetParentSpanID(pcommon.SpanID([8]byte{9})) // op.dispatch.user_turn, absent from the batch

	out := normalizeOne(t, input)
	root := findSpan(t, out, "invoke_agent codex")
	require.Equal(t, pcommon.SpanID{}, root.ParentSpanID(), "the turn is re-rooted")
	require.Equal(t, traceOne, root.TraceID(), "Codex's trace id survives")
	require.Equal(t, pcommon.SpanID([8]byte{1}), root.SpanID())
	require.Equal(t, "invoke_agent", attrString(t, root, "gen_ai.operation.name"))
	require.Equal(t, "codex", attrString(t, root, "gen_ai.agent.name"))
	require.Equal(t, "openai", attrString(t, root, "gen_ai.provider.name"))
	require.Equal(t, "thread-0001", attrString(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "gpt-5.6-luna", attrString(t, root, "gen_ai.request.model"))
	require.Equal(t, "low", attrString(t, root, "gen_ai.request.reasoning.level"))
	require.Equal(t, int64(100), attrInt(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(40), attrInt(t, root, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(7), attrInt(t, root, "gen_ai.usage.cache_write.input_tokens"))
	require.Equal(t, int64(20), attrInt(t, root, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(5), attrInt(t, root, "gen_ai.usage.reasoning.output_tokens"))
	require.Equal(t, int64(120), attrInt(t, root, "gen_ai.usage.total_tokens"))
	require.Equal(t, "native", attrString(t, root, "coding_agent.source"))
	require.Equal(t, "codex", attrString(t, root, "coding_agent.client.name"))
	require.Equal(t, "0.163.0", attrString(t, root, "coding_agent.client.version"))
	require.Equal(t, "session_task.turn", attrString(t, root, "coding_agent.source.event"))
	requireOnlyCanonical(t, root)
	_, hasNonCached := root.Attributes().Get("gen_ai.usage.non_cached_input_tokens")
	require.False(t, hasNonCached, "non_cached_input_tokens has no canonical key")
	require.Equal(t, "session_task.turn", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(), "input is not mutated")
}

func TestCompactAndReviewTurnsAreRootsToo(t *testing.T) {
	for _, name := range []string{"session_task.compact", "session_task.review"} {
		input := ptrace.NewTraces()
		turn := addTurn(newCodexGroup(input))
		turn.SetName(name)
		root := findSpan(t, normalizeOne(t, input), "invoke_agent codex")
		require.Equal(t, name, attrString(t, root, "coding_agent.source.event"))
	}
}

func TestTurnWithoutUsageOrModelStillRoots(t *testing.T) {
	input := ptrace.NewTraces()
	turn := addSpan(newCodexGroup(input), "session_task.turn", 1, 0, 0, time.Second)
	turn.Attributes().PutStr("thread.id", "thread-0002")
	root := findSpan(t, normalizeOne(t, input), "invoke_agent codex")
	_, hasModel := root.Attributes().Get("gen_ai.request.model")
	require.False(t, hasModel)
	_, hasUsage := root.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, hasUsage, "no usage on the wire, no usage keys")
}

func TestBookkeepingSpansDrop(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	addSpan(spans, "run_turn", 2, 1, time.Second, 9*time.Second)
	addSpan(spans, "responses.stream", 3, 2, time.Second, 8*time.Second)
	addSpan(spans, "codex.sampling", 4, 2, time.Second, 8*time.Second)
	out := normalizeOne(t, input)
	require.Equal(t, 1, out.Len(), "only the turn survives")
}

func TestResourceIsFiltered(t *testing.T) {
	input := ptrace.NewTraces()
	addTurn(newCodexGroup(input))
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	resource := sink.all()[0].ResourceSpans().At(0).Resource().Attributes()
	_, hasEnv := resource.Get("env")
	require.False(t, hasEnv, "non-canonical resource keys drop")
	_, hasService := resource.Get("service.name")
	require.True(t, hasService)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -v`
Expected: FAIL to compile (`New`, `ContainsCodexSpans` undefined).

- [ ] **Step 3: Write the code**

Create `normalizer.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package codextraces normalizes Codex's native tracing spans (exported with
// otel.trace_exporter) into the canonical coding-agent vocabulary. It is
// stateless: hierarchy, IDs, kinds, and status pass through; only names and
// attributes change. Codex exports its whole internal span tree, so the edge
// keeps the turn, the usage-bearing response span, and the tool spans, and
// drops everything else. Every kept span rebuilds its attribute map from
// scratch, so tracing-opentelemetry bookkeeping and codex.* keys never reach
// canonical output. The Codex logs edge (internal/codex) synthesizes the same
// trace from logs; the signals config picks which one an operator consumes.
package codextraces

import (
	"context"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

const (
	clientName = "codex"
	provider   = "openai"

	spanExec            = "codex.exec"
	dispatchPrefix      = "op.dispatch."
	spanMCPToolCall     = "mcp.tools.call"
	toolResultEventName = "codex.tool_result"
	chatMarkerAttr      = "gen_ai.usage.input_tokens"
	rootPrefix          = "invoke_agent " + clientName
)

// turnSpanNames are the session_task spans Codex opens per turn; each becomes
// an invoke_agent root.
var turnSpanNames = map[string]bool{
	"session_task.turn":    true,
	"session_task.compact": true,
	"session_task.review":  true,
}

// turnUsageKeys map the turn span's per-turn totals onto the canonical usage
// keys. non_cached_input_tokens has no canonical counterpart and drops.
var turnUsageKeys = [][2]string{
	{"codex.turn.token_usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"codex.turn.token_usage.cached_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"codex.turn.token_usage.cache_write_input_tokens", "gen_ai.usage.cache_write.input_tokens"},
	{"codex.turn.token_usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"codex.turn.token_usage.reasoning_output_tokens", "gen_ai.usage.reasoning.output_tokens"},
	{"codex.turn.token_usage.total_tokens", "gen_ai.usage.total_tokens"},
}

// chatUsageKeys map the response span's per-call counters. Four arrive under
// their canonical names already; the two codex.* spellings remap.
var chatUsageKeys = [][2]string{
	{"gen_ai.usage.input_tokens", "gen_ai.usage.input_tokens"},
	{"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_write.input_tokens"},
	{"gen_ai.usage.output_tokens", "gen_ai.usage.output_tokens"},
	{"codex.usage.reasoning_output_tokens", "gen_ai.usage.reasoning.output_tokens"},
	{"codex.usage.total_tokens", "gen_ai.usage.total_tokens"},
}

type spanKind int

const (
	kindDrop spanKind = iota
	kindRoot
	kindChat
	kindTool
)

type codexTraceNormalizer struct {
	next            consumer.Traces
	captureIdentity bool
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Codex native traces-to-traces edge.
func New(next consumer.Traces, captureIdentity bool) connector.Traces {
	return &codexTraceNormalizer{next: next, captureIdentity: captureIdentity}
}

func (*codexTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces rewrites each claimed group: kept spans are renamed and
// re-attributed, every other span drops, and kept children whose parent left
// the batch reparent onto the batch's turn root.
func (n *codexTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputRS := input.ResourceSpans().At(i)
		if !ContainsCodexSpans(inputRS) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputRS.CopyTo(rs)
		version := resourceString(rs.Resource(), "service.version")
		canonical.FilterResource(rs, n.captureIdentity)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			model := turnModel(spans)
			kinds := make(map[pcommon.SpanID]spanKind, spans.Len())
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				kinds[span.SpanID()] = normalizeSpan(span, version, model)
			}
			spans.RemoveIf(func(s ptrace.Span) bool { return kinds[s.SpanID()] == kindDrop })
			reparentOrphans(spans, kinds)
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsCodexSpans reports whether the group is a Codex export: a scope
// named after the resource's service.name (Codex's tracer takes the service
// name as its scope) holding a turn, exec, or dispatch span. The marker keeps
// unrelated Rust services whose scope happens to equal their service name
// unclaimed.
func ContainsCodexSpans(rs ptrace.ResourceSpans) bool {
	service := resourceString(rs.Resource(), "service.name")
	if service == "" {
		return false
	}
	for i := 0; i < rs.ScopeSpans().Len(); i++ {
		ss := rs.ScopeSpans().At(i)
		if ss.Scope().Name() != service {
			continue
		}
		for j := 0; j < ss.Spans().Len(); j++ {
			name := ss.Spans().At(j).Name()
			if turnSpanNames[name] || name == spanExec || strings.HasPrefix(name, dispatchPrefix) {
				return true
			}
		}
	}
	return false
}

// turnModel returns the model of the batch's earliest turn span, which the
// response spans inherit; the response span itself names no model.
func turnModel(spans ptrace.SpanSlice) string {
	model := ""
	var earliest pcommon.Timestamp
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		if !turnSpanNames[span.Name()] {
			continue
		}
		if m := firstString(span.Attributes(), "model"); m != "" && (model == "" || span.StartTimestamp() < earliest) {
			model, earliest = m, span.StartTimestamp()
		}
	}
	return model
}

// normalizeSpan classifies one wire span and, for kept kinds, replaces its
// attributes with the canonical set. Tool and chat handling live in Tasks 3
// and 4; this task keeps the turn only.
func normalizeSpan(span ptrace.Span, version, model string) spanKind {
	wire := span.Attributes()
	attrs := pcommon.NewMap()
	putCommon(attrs, version, span.Name())
	switch {
	case turnSpanNames[span.Name()]:
		span.SetName(rootPrefix)
		span.SetParentSpanID(pcommon.SpanID{})
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", clientName)
		attrs.PutStr("gen_ai.provider.name", provider)
		if thread := firstString(wire, "thread.id", "conversation.id"); thread != "" {
			attrs.PutStr("gen_ai.conversation.id", thread)
		}
		if m := firstString(wire, "model"); m != "" {
			attrs.PutStr("gen_ai.request.model", m)
		}
		if effort := firstString(wire, "codex.turn.reasoning_effort"); effort != "" {
			attrs.PutStr("gen_ai.request.reasoning.level", effort)
		}
		copyUsage(attrs, wire, turnUsageKeys)
		attrs.CopyTo(wire)
		return kindRoot
	default:
		return kindDrop
	}
}

// reparentOrphans attaches kept children whose wire parent dropped or never
// arrived onto the batch's earliest turn root. A batch without a turn (Codex
// exports children before the turn ends) leaves parents alone so the backend
// can reattach them when the turn arrives.
func reparentOrphans(spans ptrace.SpanSlice, kinds map[pcommon.SpanID]spanKind) {
	exported := make(map[pcommon.SpanID]bool, spans.Len())
	var root pcommon.SpanID
	var rootStart pcommon.Timestamp
	foundRoot := false
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		exported[span.SpanID()] = true
		if kinds[span.SpanID()] == kindRoot && (!foundRoot || span.StartTimestamp() < rootStart) {
			root, rootStart, foundRoot = span.SpanID(), span.StartTimestamp(), true
		}
	}
	if !foundRoot {
		return
	}
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
		if kinds[span.SpanID()] == kindRoot || exported[span.ParentSpanID()] {
			continue
		}
		span.SetParentSpanID(root)
	}
}

func putCommon(attrs pcommon.Map, version, wireName string) {
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	attrs.PutStr("coding_agent.source.event", wireName)
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

// copyUsage writes each present counter under its canonical key. tracing
// records integers as int64; a double or numeric string coerces the same way
// the other edges do, and a malformed value maps nothing.
func copyUsage(attrs, wire pcommon.Map, keys [][2]string) {
	for _, pair := range keys {
		if value, ok := wire.Get(pair[0]); ok {
			if n, ok := coerceInt(value); ok {
				attrs.PutInt(pair[1], n)
			}
		}
	}
}

func coerceInt(value pcommon.Value) (int64, bool) {
	switch value.Type() {
	case pcommon.ValueTypeInt:
		return value.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(value.Double()), true
	case pcommon.ValueTypeStr:
		n, err := strconv.ParseInt(value.Str(), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func firstString(attrs pcommon.Map, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs.Get(key); ok && value.Type() == pcommon.ValueTypeStr && value.Str() != "" {
			return value.Str()
		}
	}
	return ""
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*codexTraceNormalizer)(nil)
```

The unused constants (`spanMCPToolCall`, `toolResultEventName`, `chatMarkerAttr`, `chatUsageKeys`, `kindChat`, `kindTool`) serve Tasks 3 and 4; if `go vet` or the linter rejects unused package-level identifiers before then, add them in those tasks instead.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codextraces/normalizer.go connector/codingagentconnector/internal/codextraces/normalizer_test.go
git commit -m "feat(codextraces): claim Codex native trace groups and root the turn span"
```

### Task 3: The usage-bearing response span becomes `chat <model>`

**Files:**
- Change: `connector/codingagentconnector/internal/codextraces/normalizer.go` (`normalizeSpan`)
- Test: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `normalizer_test.go`:

```go
func addResponse(spans ptrace.SpanSlice, name string, id, parent byte) ptrace.Span {
	span := addSpan(spans, name, id, parent, 2*time.Second, 6*time.Second)
	span.Attributes().PutInt("gen_ai.usage.input_tokens", 50)
	span.Attributes().PutInt("gen_ai.usage.cache_read.input_tokens", 20)
	span.Attributes().PutInt("gen_ai.usage.cache_write.input_tokens", 3)
	span.Attributes().PutInt("gen_ai.usage.output_tokens", 10)
	span.Attributes().PutInt("codex.usage.reasoning_output_tokens", 2)
	span.Attributes().PutInt("codex.usage.total_tokens", 60)
	span.Attributes().PutStr("codex.request.reasoning_effort", "low")
	span.Attributes().PutStr("from", "output_item_done")
	return span
}

// TestResponseSpanBecomesChat pins the chat mapping: classification is by
// the gen_ai.usage.input_tokens attribute, whatever the wire name (Codex
// renames the span to the response event type), the model comes from the
// turn in the batch, and the codex.* usage spellings remap.
func TestResponseSpanBecomesChat(t *testing.T) {
	for _, wireName := range []string{"response.completed", "handle_responses"} {
		input := ptrace.NewTraces()
		spans := newCodexGroup(input)
		addTurn(spans)
		addSpan(spans, "run_turn", 2, 1, time.Second, 9*time.Second)
		addResponse(spans, wireName, 3, 2)

		out := normalizeOne(t, input)
		require.Equal(t, 2, out.Len())
		chat := findSpan(t, out, "chat gpt-5.6-luna")
		require.Equal(t, pcommon.SpanID([8]byte{1}), chat.ParentSpanID(), "run_turn dropped, so the chat reparents to the root")
		require.Equal(t, "chat", attrString(t, chat, "gen_ai.operation.name"))
		require.Equal(t, "openai", attrString(t, chat, "gen_ai.provider.name"))
		require.Equal(t, "gpt-5.6-luna", attrString(t, chat, "gen_ai.request.model"))
		require.Equal(t, "low", attrString(t, chat, "gen_ai.request.reasoning.level"))
		require.Equal(t, int64(50), attrInt(t, chat, "gen_ai.usage.input_tokens"))
		require.Equal(t, int64(20), attrInt(t, chat, "gen_ai.usage.cache_read.input_tokens"))
		require.Equal(t, int64(3), attrInt(t, chat, "gen_ai.usage.cache_write.input_tokens"))
		require.Equal(t, int64(10), attrInt(t, chat, "gen_ai.usage.output_tokens"))
		require.Equal(t, int64(2), attrInt(t, chat, "gen_ai.usage.reasoning.output_tokens"))
		require.Equal(t, int64(60), attrInt(t, chat, "gen_ai.usage.total_tokens"))
		require.Equal(t, wireName, attrString(t, chat, "coding_agent.source.event"))
		requireOnlyCanonical(t, chat)
		_, hasFrom := chat.Attributes().Get("from")
		require.False(t, hasFrom)
	}
}

func TestResponseWithoutTurnStaysBareChat(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	// No turn in the batch: still claimed because of the dispatch marker.
	addSpan(spans, "op.dispatch.user_turn", 7, 0, 0, 10*time.Second)
	addResponse(spans, "response.completed", 3, 7)
	out := normalizeOne(t, input)
	chat := findSpan(t, out, "chat")
	_, hasModel := chat.Attributes().Get("gen_ai.request.model")
	require.False(t, hasModel, "no turn, no model")
	require.Equal(t, pcommon.SpanID([8]byte{7}), chat.ParentSpanID(), "no root in the batch: the wire parent stays for the backend to reattach")
}

func TestResponseUsageCoercesStrings(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	span := addSpan(spans, "response.completed", 3, 1, 2*time.Second, 6*time.Second)
	span.Attributes().PutStr("gen_ai.usage.input_tokens", "50")
	span.Attributes().PutStr("codex.usage.total_tokens", "not-a-number")
	chat := findSpan(t, normalizeOne(t, input), "chat gpt-5.6-luna")
	require.Equal(t, int64(50), attrInt(t, chat, "gen_ai.usage.input_tokens"))
	_, hasTotal := chat.Attributes().Get("gen_ai.usage.total_tokens")
	require.False(t, hasTotal, "a malformed counter maps nothing")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -run 'Response' -v`
Expected: FAIL (`span "chat gpt-5.6-luna" not found`).

- [ ] **Step 3: Write the code**

In `normalizeSpan`, add a case before `default`:

```go
	case hasAttr(wire, chatMarkerAttr):
		// Codex renames the response-handling span to the response event
		// type and records the provider's usage on it when the stream
		// completes; the attribute, not the name, identifies the call.
		span.SetName("chat" + optionalNameSuffix(model))
		attrs.PutStr("gen_ai.operation.name", "chat")
		attrs.PutStr("gen_ai.provider.name", provider)
		if model != "" {
			attrs.PutStr("gen_ai.request.model", model)
		}
		if effort := firstString(wire, "codex.request.reasoning_effort"); effort != "" {
			attrs.PutStr("gen_ai.request.reasoning.level", effort)
		}
		copyUsage(attrs, wire, chatUsageKeys)
		attrs.CopyTo(wire)
		return kindChat
```

and the helpers:

```go
func hasAttr(attrs pcommon.Map, key string) bool {
	_, ok := attrs.Get(key)
	return ok
}

func optionalNameSuffix(value string) string {
	if value == "" {
		return ""
	}
	return " " + value
}
```

The chat case must come after the turn case in the `switch` so a turn span that ever carried `gen_ai.usage.input_tokens` stays a root.

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codextraces/normalizer.go connector/codingagentconnector/internal/codextraces/normalizer_test.go
git commit -m "feat(codextraces): map the usage-bearing response span to chat"
```

### Task 4: MCP calls and tool-result events become `execute_tool`

**Files:**
- Change: `connector/codingagentconnector/internal/codextraces/normalizer.go` (`normalizeSpan`)
- Test: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `normalizer_test.go`:

```go
func addToolResultEvent(span ptrace.Span, byEventAttr bool, toolName, callID string, success any) {
	event := span.Events().AppendEmpty()
	if byEventAttr {
		event.SetName("event otel/src/tool_result.rs:54")
		event.Attributes().PutStr("event.name", "codex.tool_result")
	} else {
		event.SetName("codex.tool_result")
	}
	event.SetTimestamp(span.EndTimestamp())
	event.Attributes().PutStr("tool_name", toolName)
	event.Attributes().PutStr("call_id", callID)
	event.Attributes().PutInt("duration_ms", 54)
	event.Attributes().PutInt("output_length", 120)
	switch v := success.(type) {
	case bool:
		event.Attributes().PutBool("success", v)
	case string:
		event.Attributes().PutStr("success", v)
	}
}

// TestToolResultEventBecomesExecuteTool pins the builtin tool mapping: the
// span carrying a codex.tool_result event (named directly or via its
// event.name attribute) becomes execute_tool, the event drops, and a false
// success (bool or string) marks the span failed.
func TestToolResultEventBecomesExecuteTool(t *testing.T) {
	cases := []struct {
		name        string
		byEventAttr bool
		success     any
		wantErr     bool
	}{
		{"named event, bool success", false, true, false},
		{"event.name attr, bool failure", true, false, true},
		{"string false", false, "false", true},
		{"string true", true, "true", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			spans := newCodexGroup(input)
			addTurn(spans)
			dispatch := addSpan(spans, "handle_tool_call", 5, 1, 3*time.Second, 4*time.Second)
			addToolResultEvent(dispatch, tc.byEventAttr, "exec_command", "call-1", tc.success)

			out := normalizeOne(t, input)
			tool := findSpan(t, out, "execute_tool exec_command")
			require.Equal(t, pcommon.SpanID([8]byte{1}), tool.ParentSpanID())
			require.Equal(t, "execute_tool", attrString(t, tool, "gen_ai.operation.name"))
			require.Equal(t, "exec_command", attrString(t, tool, "gen_ai.tool.name"))
			require.Equal(t, "call-1", attrString(t, tool, "gen_ai.tool.call.id"))
			require.Equal(t, "builtin", attrString(t, tool, "gen_ai.tool.type"))
			require.Equal(t, "handle_tool_call", attrString(t, tool, "coding_agent.source.event"))
			require.Equal(t, 0, tool.Events().Len(), "the tool_result event drops with its content lengths")
			if tc.wantErr {
				require.Equal(t, ptrace.StatusCodeError, tool.Status().Code())
				require.Equal(t, "tool execution failed", tool.Status().Message())
			} else {
				require.Equal(t, ptrace.StatusCodeUnset, tool.Status().Code())
			}
			requireOnlyCanonical(t, tool)
		})
	}
}

func TestMCPToolCallBecomesExecuteTool(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	mcp := addSpan(spans, "mcp.tools.call", 6, 1, 3*time.Second, 4*time.Second)
	mcp.SetKind(ptrace.SpanKindClient)
	mcp.Attributes().PutStr("tool.name", "search_issues")
	mcp.Attributes().PutStr("tool.call_id", "call-mcp-1")
	mcp.Attributes().PutStr("mcp.server.name", "github")
	mcp.Attributes().PutStr("conversation.id", "thread-0001")
	mcp.Attributes().PutStr("server.address", "mcp.example.test")
	mcp.Attributes().PutInt("server.port", 443)
	mcp.Attributes().PutStr("rpc.method", "tools/call")
	mcp.Status().SetCode(ptrace.StatusCodeError)
	mcp.Status().SetMessage("tool returned an error")

	tool := findSpan(t, normalizeOne(t, input), "execute_tool search_issues")
	require.Equal(t, ptrace.SpanKindClient, tool.Kind(), "kind passes through")
	require.Equal(t, "search_issues", attrString(t, tool, "gen_ai.tool.name"))
	require.Equal(t, "call-mcp-1", attrString(t, tool, "gen_ai.tool.call.id"))
	require.Equal(t, "mcp", attrString(t, tool, "gen_ai.tool.type"))
	require.Equal(t, "mcp.example.test", attrString(t, tool, "server.address"))
	require.Equal(t, int64(443), attrInt(t, tool, "server.port"))
	require.Equal(t, ptrace.StatusCodeError, tool.Status().Code(), "status passes through")
	_, hasServer := tool.Attributes().Get("mcp.server.name")
	require.False(t, hasServer)
	_, hasRPC := tool.Attributes().Get("rpc.method")
	require.False(t, hasRPC)
	requireOnlyCanonical(t, tool)
}

func TestToolSpanWithoutNameStaysBare(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	addTurn(spans)
	dispatch := addSpan(spans, "handle_tool_call", 5, 1, 3*time.Second, 4*time.Second)
	addToolResultEvent(dispatch, false, "", "", true)
	tool := findSpan(t, normalizeOne(t, input), "execute_tool")
	_, hasName := tool.Attributes().Get("gen_ai.tool.name")
	require.False(t, hasName)
	_, hasCall := tool.Attributes().Get("gen_ai.tool.call.id")
	require.False(t, hasCall)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -run 'Tool' -v`
Expected: FAIL (`span "execute_tool exec_command" not found`).

- [ ] **Step 3: Write the code**

In `normalizeSpan`, add two cases after the chat case and before `default`:

```go
	case span.Name() == spanMCPToolCall:
		tool := firstString(wire, "tool.name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.type", "mcp")
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
		}
		if callID := firstString(wire, "tool.call_id"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		if address := firstString(wire, "server.address"); address != "" {
			attrs.PutStr("server.address", address)
		}
		if port, ok := wire.Get("server.port"); ok {
			if n, ok := coerceInt(port); ok {
				attrs.PutInt("server.port", n)
			}
		}
		attrs.CopyTo(wire)
		return kindTool
	case hasToolResultEvent(span):
		result := toolResultEvent(span)
		tool := firstString(result.Attributes(), "tool_name")
		span.SetName("execute_tool" + optionalNameSuffix(tool))
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		attrs.PutStr("gen_ai.tool.type", "builtin")
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
		}
		if callID := firstString(result.Attributes(), "call_id"); callID != "" {
			attrs.PutStr("gen_ai.tool.call.id", callID)
		}
		if success, ok := boolValue(result.Attributes(), "success"); ok && !success {
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage("tool execution failed")
		}
		// The event carries only lengths and ids, but it is not canonical; the
		// span now says everything it said.
		span.Events().RemoveIf(func(ptrace.SpanEvent) bool { return true })
		attrs.CopyTo(wire)
		return kindTool
```

and the helpers:

```go
// isToolResultEvent recognizes Codex's trace-safe tool_result copy. The
// tracing bridge names a span event after the tracing event's name, which
// Codex sets to the event name on some paths and to "event <file>:<line>" on
// others; the event.name attribute is the stable marker.
func isToolResultEvent(event ptrace.SpanEvent) bool {
	return event.Name() == toolResultEventName || firstString(event.Attributes(), "event.name") == toolResultEventName
}

func hasToolResultEvent(span ptrace.Span) bool {
	for i := 0; i < span.Events().Len(); i++ {
		if isToolResultEvent(span.Events().At(i)) {
			return true
		}
	}
	return false
}

// toolResultEvent returns the span's first tool_result event; callers check
// hasToolResultEvent first.
func toolResultEvent(span ptrace.Span) ptrace.SpanEvent {
	for i := 0; i < span.Events().Len(); i++ {
		if isToolResultEvent(span.Events().At(i)) {
			return span.Events().At(i)
		}
	}
	return ptrace.NewSpanEvent()
}

// boolValue reads a bool attribute that tracing may have recorded as a
// string.
func boolValue(attrs pcommon.Map, key string) (bool, bool) {
	value, ok := attrs.Get(key)
	if !ok {
		return false, false
	}
	switch value.Type() {
	case pcommon.ValueTypeBool:
		return value.Bool(), true
	case pcommon.ValueTypeStr:
		b, err := strconv.ParseBool(value.Str())
		return b, err == nil
	default:
		return false, false
	}
}
```

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codextraces/normalizer.go connector/codingagentconnector/internal/codextraces/normalizer_test.go
git commit -m "feat(codextraces): map MCP calls and tool results to execute_tool"
```

### Task 5: Orphans, split batches, and the fixture replay

**Files:**
- Test: `connector/codingagentconnector/internal/codextraces/normalizer_test.go`
- Create: `connector/codingagentconnector/internal/codextraces/fixtures_test.go`
- Create: `connector/codingagentconnector/internal/codextraces/conformance_test.go`
- Create: `connector/codingagentconnector/internal/codextraces/testdata/codex-traces-canonical.otlp.json`

- [ ] **Step 1: Write the orphan and split-batch tests**

Append to `normalizer_test.go`:

```go
// TestChildrenKeepParentsInTurnlessBatch pins the split-batch rule: Codex
// exports a child when it ends, so chat and tool spans can arrive before
// their turn. Without a root in the batch the edge leaves parent ids alone;
// the next batch's turn carries the same trace id and the backend reattaches.
func TestChildrenKeepParentsInTurnlessBatch(t *testing.T) {
	first := ptrace.NewTraces()
	spans := newCodexGroup(first)
	addSpan(spans, "op.dispatch.user_turn", 9, 0, 0, 10*time.Second)
	addResponse(spans, "response.completed", 3, 2)
	dispatch := addSpan(spans, "handle_tool_call", 5, 2, 3*time.Second, 4*time.Second)
	addToolResultEvent(dispatch, false, "exec_command", "call-1", true)
	out := normalizeOne(t, first)
	require.Equal(t, 2, out.Len())
	require.Equal(t, pcommon.SpanID([8]byte{2}), findSpan(t, out, "chat").ParentSpanID())
	require.Equal(t, pcommon.SpanID([8]byte{2}), findSpan(t, out, "execute_tool exec_command").ParentSpanID())

	second := ptrace.NewTraces()
	addTurn(newCodexGroup(second))
	root := findSpan(t, normalizeOne(t, second), "invoke_agent codex")
	require.Equal(t, traceOne, root.TraceID(), "same trace id joins the batches downstream")
}

func TestOrphansReparentToEarliestTurn(t *testing.T) {
	input := ptrace.NewTraces()
	spans := newCodexGroup(input)
	late := addTurn(spans)
	late.SetSpanID(pcommon.SpanID([8]byte{11}))
	late.SetStartTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(20 * time.Second)))
	late.SetEndTimestamp(pcommon.NewTimestampFromTime(baseTime.Add(30 * time.Second)))
	early := addTurn(spans)
	addResponse(spans, "response.completed", 3, 2)
	out := normalizeOne(t, input)
	require.Equal(t, early.SpanID(), findSpan(t, out, "chat gpt-5.6-luna").ParentSpanID())
}
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -count=1`
Expected: PASS (`reparentOrphans` picks the root with the smallest `StartTimestamp()`, so slice order does not matter).

- [ ] **Step 2: Write the fixture replay and conformance tests**

Create `fixtures_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// loadFixtureTraces merges the JSONL capture (one OTLP export per line) the
// way a trace backend would; the conformance test shares the loader.
func loadFixtureTraces(t *testing.T) ptrace.Traces {
	t.Helper()
	raw, err := loadNativeFixtureTraces()
	require.NoError(t, err)
	return raw.Traces
}

func replayFixture(t *testing.T) ptrace.Traces {
	t.Helper()
	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), loadFixtureTraces(t)))
	require.Len(t, sink.all(), 1)
	return sink.all()[0]
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	actual, err := (&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "codex-traces-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

// TestFixtureReplayShape pins what the live capture must yield: one root per
// turn, a chat child with usage and the turn's model, and at least one
// execute_tool child, with nothing outside the vocabulary.
func TestFixtureReplayShape(t *testing.T) {
	out := replayFixture(t)
	roots, chats, tools := 0, 0, 0
	for i := 0; i < out.ResourceSpans().Len(); i++ {
		spans := out.ResourceSpans().At(i).ScopeSpans().At(0).Spans()
		for j := 0; j < spans.Len(); j++ {
			span := spans.At(j)
			switch attrString(t, span, "gen_ai.operation.name") {
			case "invoke_agent":
				roots++
				require.NotEmpty(t, attrString(t, span, "gen_ai.conversation.id"))
			case "chat":
				chats++
				require.NotEmpty(t, attrString(t, span, "gen_ai.request.model"))
				attrInt(t, span, "gen_ai.usage.input_tokens")
			case "execute_tool":
				tools++
			}
			requireOnlyCanonical(t, span)
		}
	}
	require.GreaterOrEqual(t, roots, 1)
	require.GreaterOrEqual(t, chats, 1)
	require.GreaterOrEqual(t, tools, 1)
}
```

Create `conformance_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codextraces

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/canonical"
)

// TestCodexTracesConformance pins the edge to the canonical contract over the
// live capture: required keys, nothing outside the vocabulary, and the
// per-call usage on the response span accounted for on chat spans.
func TestCodexTracesConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "codextraces",
		LoadRaw:   loadNativeFixtureTraces,
		Normalize: normalizeViaNormalizer,
		Signals:   codexTracesSignals(),
	})
}

// codexTracesSignals is shared with the cross-harness registry. The turn's
// totals and the response span's per-call counters both land on canonical
// keys, so sums are restricted to the response span versus chat output; the
// turn's codex.turn.* keys are checked by presence.
func codexTracesSignals() []canonical.Signal {
	return []canonical.Signal{
		{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
		{RawKey: "gen_ai.usage.output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Presence},
		{RawKey: "codex.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Presence},
		{RawKey: "codex.turn.token_usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
		{RawKey: "codex.turn.reasoning_effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
		{RawKey: "thread.id", CanonicalKey: "gen_ai.conversation.id", Kind: canonical.Presence},
	}
}

// loadNativeFixtureTraces parses the JSONL OTLP capture of a real Codex run
// (one export per line) into one raw Traces value.
func loadNativeFixtureTraces() (canonical.RawInput, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "codex-native-traces.json"))
	if err != nil {
		return canonical.RawInput{}, err
	}
	unmarshaler := &ptrace.JSONUnmarshaler{}
	traces := ptrace.NewTraces()
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		batch, err := unmarshaler.UnmarshalTraces(line)
		if err != nil {
			return canonical.RawInput{}, err
		}
		batch.ResourceSpans().MoveAndAppendTo(traces.ResourceSpans())
	}
	return canonical.RawInput{Traces: traces}, nil
}

func normalizeViaNormalizer(raw canonical.RawInput) (ptrace.Traces, error) {
	sink := &traceSink{}
	if err := New(sink, true).ConsumeTraces(context.Background(), raw.Traces); err != nil {
		return ptrace.Traces{}, err
	}
	all := ptrace.NewTraces()
	for _, traces := range sink.all() {
		traces.ResourceSpans().MoveAndAppendTo(all.ResourceSpans())
	}
	return all, nil
}
```

The conformance file's imports are `bytes`, `context`, `os`, `path/filepath`, `testing`, `ptrace`, and `canonical`; the fixtures file no longer needs `bytes` or `os` beyond `path/filepath` for the canonical fixture read.

Sum signals are deliberately avoided here: the raw input carries each count twice (turn totals and per-call counters) under keys that both land on the same canonical key, so a raw-vs-output sum cannot balance. Presence pins that every source reaches output.

- [ ] **Step 3: Generate the canonical fixture**

Create a throwaway `regen_test.go`:

```go
package codextraces

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestRegenerateCanonicalFixture(t *testing.T) {
	out, err := (&ptrace.JSONMarshaler{}).MarshalTraces(replayFixture(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "codex-traces-canonical.otlp.json"), out, 0o644))
}
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -run TestRegenerateCanonicalFixture -count=1`, then `rm` the file. Inspect the output with the Python snippet from Task 1 Step 3 pointed at the canonical file: every span name must start with `invoke_agent codex`, `chat`, or `execute_tool`, and no attribute key may start with `codex.`, `code.`, or `thread.`.

- [ ] **Step 4: Run the package**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codextraces/ -count=1`
Expected: PASS. If `TestFixtureReplayShape` fails on `tools >= 1`, the capture's tool_result event rides a span the slice dropped; widen the jq `kept` filter in Task 1 to the event's carrier and re-slice.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codextraces/normalizer_test.go connector/codingagentconnector/internal/codextraces/fixtures_test.go connector/codingagentconnector/internal/codextraces/conformance_test.go connector/codingagentconnector/internal/codextraces/testdata/codex-traces-canonical.otlp.json
git commit -m "test(codextraces): pin split batches, the fixture replay, and conformance"
```

### Task 6: Wire the edge into the traces router

**Files:**
- Change: `connector/codingagentconnector/traces.go`
- Change: `connector/codingagentconnector/internal/genai/normalizer.go:70-85` (defer list)
- Change: `connector/codingagentconnector/conformance_test.go` (registry and `wiredPipelines`)
- Test: `connector/codingagentconnector/traces_test.go`

- [ ] **Step 1: Write the failing router test**

Append to `traces_test.go`:

```go
func TestTracesRouterClaimsCodexNativeGroup(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "codex_exec")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("codex_exec")
	turn := ss.Spans().AppendEmpty()
	turn.SetName("session_task.turn")
	turn.SetTraceID(pcommon.TraceID([16]byte{5}))
	turn.SetSpanID(pcommon.SpanID([8]byte{6}))
	turn.Attributes().PutStr("thread.id", "thread-router")

	sink := &routerSink{}
	require.NoError(t, newTracesRouter(createDefaultConfig(), sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.traces, 1, "exactly one edge claims a Codex group")
	spans := sink.traces[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.Equal(t, 1, spans.Len())
	require.Equal(t, "invoke_agent codex", spans.At(0).Name())
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test . -run TestTracesRouterClaimsCodexNativeGroup -v`
Expected: FAIL (`sink.traces` empty).

- [ ] **Step 3: Wire it**

In `traces.go`, import `".../internal/codextraces"` and extend the constructor:

```go
	return &tracesRouter{edges: []connector.Traces{
		claude.New(next, id), genai.New(next, id), opencode.New(next, id), pi.New(next, id), openhands.New(next, id), codextraces.New(next, id),
	}, consume: cfg.ConsumesTraces()}
```

(if plan 1 has not landed, omit `consume:`). Update the type comment to list the Codex edge: "the Codex native edge claims groups whose scope equals the service name and holds a turn span".

The GenAI edge's scope prefixes (`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`, `strands.telemetry`, `github.copilot`) can never equal a Codex service name, so the defer list needs no entry; add a one-line comment to that effect next to the existing defer checks in `internal/genai/normalizer.go`.

In `conformance_test.go`:

- add `"codextraces"` to `wiredPipelines` after `"openhands"` (keep the constructor order);
- add a fixture entry:

```go
		{wired: "codextraces", edge: canonical.Edge{
			Name: "codextraces",
			LoadRaw: func() (canonical.RawInput, error) {
				return conformanceLoadJSONLTraces("internal/codextraces/testdata/codex-native-traces.json")
			},
			Normalize: conformanceConsumeTraces(codextraces.New),
			Signals: []canonical.Signal{
				{RawKey: "gen_ai.usage.input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Presence},
				{RawKey: "codex.usage.total_tokens", CanonicalKey: "gen_ai.usage.total_tokens", Kind: canonical.Presence},
				{RawKey: "codex.turn.reasoning_effort", CanonicalKey: "gen_ai.request.reasoning.level", Kind: canonical.Presence},
				{RawKey: "thread.id", CanonicalKey: "gen_ai.conversation.id", Kind: canonical.Presence},
			},
		}},
```

and update the `TestFixtureCoverageAcceptsMultiEdgeFixtures` comment ("ten fixture edges over eight wired pipelines").

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test . -count=1`
Expected: PASS, including `TestCrossHarnessConformanceRegistry` (eight wired pipelines, each with a fixture).

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/traces.go connector/codingagentconnector/traces_test.go connector/codingagentconnector/conformance_test.go connector/codingagentconnector/internal/genai/normalizer.go
git commit -m "feat(router): wire the Codex native traces edge"
```

### Task 7: Logs edge maps `call_id`

**Files:**
- Change: `connector/codingagentconnector/internal/codex/trace.go:197-228` (`appendToolSpans`)
- Test: `connector/codingagentconnector/internal/codex/trace_test.go`
- Change: `connector/codingagentconnector/internal/codex/conformance_test.go`, `connector/codingagentconnector/conformance_test.go` (codex signals)
- Change: `connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json`

- [ ] **Step 1: Write the failing test**

Append to `internal/codex/trace_test.go`:

```go
func TestToolSpanCarriesCallID(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	turn := &turnState{
		conversationID: "conversation-1", first: base, last: base.Add(2 * time.Second), promptSeen: true,
		resource: map[string]any{"service.name": "codex_cli_rs"},
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.tool_result", base.Add(time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1"}),
			testEvent("codex.tool_result", base.Add(2*time.Second), map[string]any{"tool_name": "read_file"}),
		},
	}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.Equal(t, "call-1", attrString(t, findSpan(t, spans, "execute_tool shell"), "gen_ai.tool.call.id"))
	_, ok := findSpan(t, spans, "execute_tool read_file").Attributes().Get("gen_ai.tool.call.id")
	require.False(t, ok, "no call_id on the wire, no key")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codex/ -run TestToolSpanCarriesCallID -v`
Expected: FAIL.

- [ ] **Step 3: Write the code**

In `appendToolSpans`, after `span.Attributes().PutStr("coding_agent.source.event", event.name)`, add:

```go
		if callID != "" {
			span.Attributes().PutStr("gen_ai.tool.call.id", callID)
		}
```

- [ ] **Step 4: Update the canonical fixture and signals**

The native fixture's two `tool_result` records carry `call_id` values. Add `gen_ai.tool.call.id` to the canonical fixture's two `execute_tool` spans with the matching values. From the repo root:

```bash
python3 -I - <<'EOF'
import json
native = 'connector/codingagentconnector/internal/codex/testdata/codex-native-logs.json'
canon = 'connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json'
calls = []
for line in open(native):
    if not line.strip(): continue
    for rl in json.loads(line)['resourceLogs']:
        for sl in rl['scopeLogs']:
            for r in sl['logRecords']:
                a = {x['key']: x['value'].get('stringValue') for x in r.get('attributes', [])}
                if a.get('event.name') == 'codex.tool_result':
                    calls.append((a.get('tool_name'), a.get('call_id')))
print('tool results in native fixture:', calls)
out = []
for line in open(canon):
    if not line.strip():
        out.append(line); continue
    d = json.loads(line)
    for rs in d['resourceSpans']:
        for ss in rs['scopeSpans']:
            for s in ss['spans']:
                if not s['name'].startswith('execute_tool'): continue
                tool = s['name'].split(' ', 1)[1]
                for name, call in calls:
                    if name == tool and call and not any(x['key'] == 'gen_ai.tool.call.id' for x in s['attributes']):
                        s['attributes'].append({'key': 'gen_ai.tool.call.id', 'value': {'stringValue': call}})
                        break
    out.append(json.dumps(d, separators=(',', ':')) + '\n')
open(canon, 'w').writelines(out)
EOF
```

Check `git diff` shows exactly two added attributes (match the file's original separators if the diff reformats it). In both `internal/codex/conformance_test.go` and the registry's `codex` fixture, append:

```go
{RawKey: "call_id", CanonicalKey: "gen_ai.tool.call.id", Kind: canonical.Presence},
```

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test ./... -count=1`; from the repo root: `GOTOOLCHAIN=go1.27.0 go test ./e2e/validator/ -run Codex -count=1`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codex/trace.go connector/codingagentconnector/internal/codex/trace_test.go connector/codingagentconnector/internal/codex/conformance_test.go connector/codingagentconnector/conformance_test.go connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json
git commit -m "feat(codex): map tool_result call_id to gen_ai.tool.call.id"
```

### Task 8: Validator asserts both Codex roots in the live run

**Files:**
- Change: `e2e/validator/validator.go`
- Change: `e2e/validator/live_test.go`
- Test: `e2e/validator/validator_test.go`

- [ ] **Step 1: Write the failing test**

Append to `e2e/validator/validator_test.go` (it has helpers that build canonical traces by hand around line 420; reuse the pattern that builds an `invoke_agent codex` root with a chat child):

```go
// TestValidateCodexNativeTracesRequiresNativeRoot pins the live-run check for
// the Codex traces edge: a run must hold an invoke_agent codex root with
// coding_agent.source=native, a chat child with usage, and an execute_tool
// child; the logs edge's normalized root alone does not satisfy it.
func TestValidateCodexNativeTracesRequiresNativeRoot(t *testing.T) {
	runID := "codex-otel-test"
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", runID)
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	addRoot := func(source string, id byte) ptrace.Span {
		root := spans.AppendEmpty()
		root.SetName("invoke_agent codex")
		root.SetTraceID(pcommon.TraceID([16]byte{id}))
		root.SetSpanID(pcommon.SpanID([8]byte{id}))
		root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		root.Attributes().PutStr("gen_ai.conversation.id", "conv")
		root.Attributes().PutStr("coding_agent.source", source)
		root.Attributes().PutStr("coding_agent.client.name", "codex")
		return root
	}
	addChild := func(root ptrace.Span, operation string, id byte) ptrace.Span {
		child := spans.AppendEmpty()
		child.SetName(operation)
		child.SetTraceID(root.TraceID())
		child.SetSpanID(pcommon.SpanID([8]byte{id}))
		child.SetParentSpanID(root.SpanID())
		child.Attributes().PutStr("gen_ai.operation.name", operation)
		child.Attributes().PutStr("coding_agent.source", "native")
		child.Attributes().PutStr("coding_agent.client.name", "codex")
		return child
	}

	normalized := addRoot("normalized", 1)
	addChild(normalized, "chat", 2).Attributes().PutInt("gen_ai.usage.input_tokens", 1)
	addChild(normalized, "execute_tool", 3)
	require.ErrorContains(t, validateCodexNativeTraces(traces, runID), "native")

	native := addRoot("native", 4)
	addChild(native, "chat", 5).Attributes().PutInt("gen_ai.usage.input_tokens", 1)
	require.ErrorContains(t, validateCodexNativeTraces(traces, runID), "execute_tool")
	addChild(native, "execute_tool", 6)
	require.NoError(t, validateCodexNativeTraces(traces, runID))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run from the repo root: `GOTOOLCHAIN=go1.27.0 go test ./e2e/validator/ -run TestValidateCodexNativeTracesRequiresNativeRoot -v`
Expected: FAIL to compile (`validateCodexNativeTraces` undefined).

- [ ] **Step 3: Write the code**

Add to `e2e/validator/validator.go`:

```go
// validateCodexNativeFile asserts the Codex traces edge produced a canonical
// trace beside the logs edge's: the e2e stack exports both signals, so a run
// carries two invoke_agent codex roots per turn, one per edge.
func validateCodexNativeFile(path, runID string) error {
	return validateTraceFile(path, runID, validateCodexNativeTraces)
}

func validateCodexNativeTraces(traces ptrace.Traces, runID string) error {
	spans := collectRunSpans(traces, runID)
	return firstValidRoot(spans, "invoke_agent codex", "native codex root was not found", func(root ptrace.Span) error {
		if stringAttr(root, "coding_agent.source") != "native" {
			return errors.New("codex root is not from the native traces edge")
		}
		if stringAttr(root, "gen_ai.conversation.id") == "" {
			return errors.New("native codex root has no conversation id")
		}
		chat, tool := false, false
		for _, child := range spans {
			if child.ParentSpanID() != root.SpanID() || child.TraceID() != root.TraceID() {
				continue
			}
			switch stringAttr(child, "gen_ai.operation.name") {
			case "chat":
				if _, ok := child.Attributes().Get("gen_ai.usage.input_tokens"); ok {
					chat = true
				}
			case "execute_tool":
				tool = true
			}
		}
		if !chat {
			return errors.New("native codex root has no chat child with usage")
		}
		if !tool {
			return errors.New("native codex root has no execute_tool child")
		}
		return nil
	})
}
```

In `e2e/validator/live_test.go`, after the `openhands` raw-file check in the polling loop, add:

```go
		if lastErr == nil && agent == "codex" {
			lastErr = validateCodexNativeFile(path, runID)
		}
```

- [ ] **Step 4: Run the validator tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./e2e/validator/ -count=1`
Expected: PASS (`TestCodexCanonicalFixtureValidates` still passes: the fixture check does not call the native validator).

- [ ] **Step 5: Commit**

```bash
git add e2e/validator/validator.go e2e/validator/validator_test.go e2e/validator/live_test.go
git commit -m "test(e2e): require the Codex native traces root in the live run"
```

### Task 9: Docs, full check, and the live run

**Files:**
- Change: `docs/harnesses/codex.md` (Signal support table, Traces section)
- Change: `docs/otel-signals.md:36`, `docs/harnesses.md:33`, `README.md:15,82,247`

- [ ] **Step 1: Codex matrix**

In `docs/harnesses/codex.md`:

- Signal support table, Traces row: change `none (no traces edge for Codex spans; the Logs edge synthesizes the canonical trace)` to `traces edge (this file's Traces → Connector mapping; off by default upstream)`.
- Replace the "Traces → Connector mapping" section ("None. The connector has no traces edge …") with:

```markdown
### Connector mapping

The connector claims a resource group whose instrumentation scope equals the
resource `service.name` (Codex's tracer takes the service name as its scope:
`codex_exec` for `codex exec`) and that holds a `session_task.*`,
`codex.exec`, or `op.dispatch.*` span. It keeps three span families, rebuilds
their attributes from scratch, and drops every other span (tracing-opentelemetry
bookkeeping included). IDs, kinds, and status pass through; the trace id is
Codex's own, which is per turn, so one conversation is one canonical trace per
turn joined on `gen_ai.conversation.id`.

Running Codex logs and traces into one connector produces two canonical traces
per turn, one from each edge (`coding_agent.source` = `normalized` from logs,
`native` from traces). Pick one with the connector's `signals` setting or by
pipeline wiring; the connector does not dedupe across signals. The e2e stack
runs both on purpose.

| Raw span | Canonical span name |
|---|---|
| `session_task.turn` (also `.compact`, `.review`) | `invoke_agent codex` (parent cleared) |
| the response-handling span carrying `gen_ai.usage.input_tokens` (Codex names it after the response event type) | `chat <model>` |
| `mcp.tools.call` | `execute_tool <tool.name>` |
| any span carrying a `codex.tool_result` event | `execute_tool <tool_name>` |
| everything else (`run_turn`, `responses.stream`, `codex.sampling`, `handle_tool_call` without a result event, …) | dropped |

#### Attribute matrix

##### session_task.turn → invoke_agent

| Raw key | Canonical key | Status |
|---|---|---|
| `thread.id` | `gen_ai.conversation.id` | mapped (absent when the span carries neither `thread.id` nor `conversation.id`) |
| `model` | `gen_ai.request.model` | mapped (also supplies the chat spans' model; absent when the turn span carries none) |
| `codex.turn.reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (absent when no effort applies) |
| `codex.turn.token_usage.input_tokens` | `gen_ai.usage.input_tokens` | mapped (each counter only when the span carries it; Codex records them when the task finishes, so a turn exported before then has none) |
| `codex.turn.token_usage.cached_input_tokens` | `gen_ai.usage.cache_read.input_tokens` | mapped (same condition) |
| `codex.turn.token_usage.cache_write_input_tokens` | `gen_ai.usage.cache_write.input_tokens` | mapped (same condition) |
| `codex.turn.token_usage.output_tokens` | `gen_ai.usage.output_tokens` | mapped (same condition) |
| `codex.turn.token_usage.reasoning_output_tokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (same condition) |
| `codex.turn.token_usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (same condition) |
| `codex.turn.token_usage.non_cached_input_tokens`, `turn.id` | — | dropped |
| `code.*`, `thread.name`, `busy_ns`, `idle_ns`, `target`, `level` | — | dropped (tracing-opentelemetry bookkeeping on every span) |

##### response span → chat

| Raw key | Canonical key | Status |
|---|---|---|
| `gen_ai.usage.input_tokens`, `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_write.input_tokens`, `gen_ai.usage.output_tokens` | same | kept (recorded when the stream completes; the input-token key is also the span's discriminator) |
| `codex.usage.reasoning_output_tokens` | `gen_ai.usage.reasoning.output_tokens` | mapped (absent when the provider reports none) |
| `codex.usage.total_tokens` | `gen_ai.usage.total_tokens` | mapped (same) |
| `codex.request.reasoning_effort` | `gen_ai.request.reasoning.level` | mapped (absent when no effort applies) |
| (turn span's `model`) | `gen_ai.request.model` | mapped (absent, and the span named bare `chat`, when the batch carries no turn span) |
| `from`, `tool_name` | — | dropped |

##### mcp.tools.call → execute_tool

| Raw key | Canonical key | Status |
|---|---|---|
| `tool.name` | `gen_ai.tool.name` | mapped (also names the span) |
| `tool.call_id` | `gen_ai.tool.call.id` | mapped |
| `server.address`, `server.port` | same | kept (absent for stdio servers) |
| record kind | `gen_ai.tool.type` = `mcp` | mapped |
| `mcp.server.name`, `mcp.server.origin`, `mcp.transport`, `mcp.connector.*`, `codex.mcp.*`, `rpc.*`, `conversation.id`, `session.id`, `turn.id`, `error.type` | — | dropped (span Status passes through) |

##### codex.tool_result event → execute_tool

| Raw key (event attribute) | Canonical key | Status |
|---|---|---|
| `tool_name` | `gen_ai.tool.name` | mapped (also names the span; absent when the event carries none) |
| `call_id` | `gen_ai.tool.call.id` | mapped (absent when empty) |
| record kind | `gen_ai.tool.type` = `builtin` | mapped |
| `success` = false (bool or string) | span Status = Error, message `tool execution failed` | mapped to status |
| `duration_ms`, `tool_namespace`, `tool_result_seq`, `output_truncated`, `arguments_length`, `output_length`, `output_line_count`, `tool_origin`, `mcp_tool`, `product_sku` | — | dropped (the whole event drops) |

#### Connector-written attributes

- `coding_agent.source` = `native`
- `coding_agent.client.name` = `codex`
- `coding_agent.client.version` = resource `service.version` (when present)
- `coding_agent.source.event` = the wire span name
- `gen_ai.operation.name` = `invoke_agent` / `chat` / `execute_tool`
- `gen_ai.agent.name` = `codex` (root only)
- `gen_ai.provider.name` = `openai` (root and chat spans)

#### Canonical keys with no source on the traces wire

| Canonical key | Status |
|---|---|
| `gen_ai.response.time_to_first_chunk` | not provided (the usage-bearing `response.completed` log event, which carries `ttft_ms`, never reaches traces; use the logs edge for TTFT) |
| `coding_agent.request.service_tier` | not provided (same reason) |
| `coding_agent.user.id` / `coding_agent.user.email` / `coding_agent.terminal.type` | not provided (trace-safe events omit identity and the spans carry none) |
| `gen_ai.response.finish_reasons` / `gen_ai.response.id` / `gen_ai.response.model` | not provided |
| `coding_agent.usage.cost` | not provided (`codex.turn_cost` is log-only) |
```

- Replace the sentence "The e2e config sets only the log `exporter`, so the pinned capture has no native spans." with "The e2e config sets both `exporter` and `trace_exporter`, so the Codex stack exercises both edges; the traces fixture is `internal/codextraces/testdata/codex-native-traces.json`." and update the Sources list with the capture date.
- In the Logs section's `codex.tool_result` table, change the `call_id` row to `| `call_id` | `gen_ai.tool.call.id` | mapped (absent when the record carries none) |`, and drop `gen_ai.tool.call.id` from the "Canonical keys with no Codex source" row.

- [ ] **Step 2: Summary docs**

- `docs/otel-signals.md` line 36, Codex row, last column: `logs (synthesizes traces) and traces (native edge)`.
- `docs/harnesses.md` line 33, Codex row, last column: `yes (logs edge and traces edge)`.
- `README.md`: line 15's Codex bullet gains "; with `otel.trace_exporter` set, its native spans normalize through the traces edge too"; the harness table row (line 82) gets a second row `| Codex (native traces) | traces | resource groups whose scope equals `service.name` and hold a `session_task.*` span | `trace_exporter` in the same `[otel]` block |`; line 247 lists Codex among the auto-detected traces sources.

- [ ] **Step 3: Full check**

Run from the repo root: `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`.

- [ ] **Step 4: Live run**

```bash
GOTOOLCHAIN=go1.27.0 ./scripts/e2e.sh
```

Expected: the validator passes, now including `validateCodexNativeFile`. Confirm both roots:

```bash
python3 -I - <<'EOF'
import json, collections
c = collections.Counter()
for line in open('.e2e-output/canonical-traces.json'):
    if not line.strip(): continue
    for rs in json.loads(line)['resourceSpans']:
        for ss in rs['scopeSpans']:
            for s in ss['spans']:
                if s['name'] == 'invoke_agent codex':
                    c[{a['key']: a['value'].get('stringValue') for a in s['attributes']}.get('coding_agent.source')] += 1
print(c)
EOF
```

Expected: both `normalized` and `native` counts are at least 1.

- [ ] **Step 5: Commit and open the PR**

```bash
git add docs/harnesses/codex.md docs/otel-signals.md docs/harnesses.md README.md
git commit -m "docs(codex): document the native traces edge"
```

Push and open a PR titled `feat(codex): native traces edge` with the spec path, the Task 1 wire findings, and the live-run root counts in the body.
