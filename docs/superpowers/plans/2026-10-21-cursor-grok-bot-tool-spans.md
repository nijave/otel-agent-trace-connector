# Cursor Grok Bot Tool Spans Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn Cursor's Grok Bot `grok_bot_tool_result` and `grok_bot_mcp_tool_call` log records into `execute_tool` child spans, and record the `cloud_agent_artifact` event rename in the Cursor matrix.

**Architecture:** The Cursor logs edge already synthesizes one `invoke_agent cursor` root per burst with `chat` children per `api_request`; a new `appendToolSpans` in `internal/cursor/trace.go` adds one `execute_tool` child per tool row, built the same way the Codex edge builds its tool spans (deterministic span id, start = timestamp minus duration). The two record names leave the root-event path. A hand-built fixture extension covers it because Cursor has no live stack.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`/`pcommon`/`plog`), testify.

**Spec:** `docs/superpowers/specs/2026-10-19-harness-feature-gaps-design.md`, sections 4 and 6 (artifact rename). Read it first.

## Global Constraints

- Work from `connector/codingagentconnector/` for Go commands; prefix every `go` command with `GOTOOLCHAIN=go1.27.0`.
- `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh` from the repo root must end `ALL CHECKS PASSED` before pushing.
- Tool spans carry only `gen_ai.operation.name`, `gen_ai.tool.name`, `gen_ai.tool.call.id` (when non-empty), `gen_ai.tool.type` (`builtin` or `mcp`), `coding_agent.source`, `coding_agent.client.name`, `coding_agent.source.event`. Every other `cursor.*` attribute drops.
- Status Error with message `tool execution failed` only when the builtin row's `cursor.grok_bot.tool_result.outcome` is `error` or the MCP row's `cursor.tool.status` is `failure`. `denied`, `cancelled`, and `success` leave Status unset.
- Event names come from `Event.Name` (PR #104's wire-event-name rule), never from the record body.
- Fixture values stay synthetic. Stage explicit paths only.
- Cursor has no live stack; unit fixtures are the only check. Do not add a validator rule for Cursor tool spans.

## Review Focus

1. **A tool row without a duration must not produce a negative or wrapped span.** Task 1 tests a row with no `duration_ms` (zero-length span) and reuses the Codex-style guard against overflowing values.
2. **A builtin row that re-runs after a stream failure yields two spans sharing a call id, not one.** Span ids derive from `cursor.event.id`, which differs per row. Task 1 tests two rows with one `tool_call.id`.
3. **A `denied` outcome must not read as a failure.** Task 1 asserts Status Unset for `denied`.
4. **Tool rows must never also land as root events.** Task 2 asserts the root has no `grok_bot_tool_result` event once spans exist.
5. **A record missing `cursor.tool.name` still produces a span, named bare `execute_tool`, so a tool call is never silently lost.** Task 1 tests it.

---

### Task 1: `appendToolSpans` for the two Grok Bot rows

**Files:**
- Change: `connector/codingagentconnector/internal/cursor/event.go` (constants near `NameAPIError`)
- Change: `connector/codingagentconnector/internal/cursor/trace.go` (new function; call from `buildTrace` after `appendChatSpans`)
- Test: `connector/codingagentconnector/internal/cursor/trace_test.go`

**Interfaces:**
- Produces: `cursor.NameGrokBotToolResult = "grok_bot_tool_result"`, `cursor.NameGrokBotMCPToolCall = "grok_bot_mcp_tool_call"`, `func IsToolEvent(name string) bool`, `func appendToolSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []Event)`.

- [ ] **Step 1: Write the failing tests**

Append to `connector/codingagentconnector/internal/cursor/trace_test.go` (it has `testConversation`, `testResourceRaw`, `mustBuildTrace`, `spansByName`, `burstForTest`):

```go
func grokBotBurst() *burstState {
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Name: NameAPIRequest, EventID: "ev-1", ConversationID: testConversation, UsageEventID: "ue-1",
			Timestamp: base, Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.api.request.input_tokens": int64(1), "cursor.api.request.output_tokens": int64(2)}},
		{Name: NameGrokBotToolResult, EventID: "ev-t1", ConversationID: testConversation,
			Timestamp: base.Add(2 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.tool.name": "shell", "cursor.grok_bot.tool_call.id": "call-1",
				"cursor.grok_bot.tool_result.outcome": "error", "cursor.grok_bot.tool_result.duration_ms": int64(1500),
				"cursor.grok_bot.tool_result.error_category": "timeout", "cursor.grok_bot.turn.id": "turn-1",
			}},
		{Name: NameGrokBotMCPToolCall, EventID: "ev-t2", ConversationID: testConversation,
			Timestamp: base.Add(3 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.tool.name": "search_issues", "cursor.tool.status": "success",
				"cursor.grok_bot.tool_call.id": "call-2", "cursor.grok_bot.mcp.duration_ms": int64(250),
				"cursor.mcp.server.name": "github",
			}},
		// No duration, no call id, denied: a zero-length span with Status unset.
		{Name: NameGrokBotToolResult, EventID: "ev-t3", ConversationID: testConversation,
			Timestamp: base.Add(4 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.tool.name": "read", "cursor.grok_bot.tool_result.outcome": "denied"}},
		// A re-run after a stream failure: same call id as ev-t1, its own event id.
		{Name: NameGrokBotToolResult, EventID: "ev-t4", ConversationID: testConversation,
			Timestamp: base.Add(5 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.tool.name": "shell", "cursor.grok_bot.tool_call.id": "call-1",
				"cursor.grok_bot.tool_result.outcome": "success", "cursor.grok_bot.tool_result.duration_ms": int64(100)}},
		// An MCP failure.
		{Name: NameGrokBotMCPToolCall, EventID: "ev-t5", ConversationID: testConversation,
			Timestamp: base.Add(6 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.tool.name": "create_pr", "cursor.tool.status": "failure", "cursor.grok_bot.mcp.duration_ms": int64(10)}},
		// No tool name at all.
		{Name: NameGrokBotToolResult, EventID: "ev-t6", ConversationID: testConversation,
			Timestamp: base.Add(7 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.grok_bot.tool_result.outcome": "success"}},
	}
	return &burstState{
		conversationID: testConversation, events: events, seen: map[string]struct{}{},
		resource: testResourceRaw(), first: base, last: base.Add(7 * time.Second), lastSeen: base.Add(7 * time.Second),
	}
}

func TestBuildTraceEmitsGrokBotToolSpans(t *testing.T) {
	traces := mustBuildTrace(t, grokBotBurst(), "quiet")
	byName := spansByName(traces)
	root := byName["invoke_agent cursor"][0]

	shells := byName["execute_tool shell"]
	require.Len(t, shells, 2, "a re-run after a stream failure is its own span")
	require.NotEqual(t, shells[0].SpanID(), shells[1].SpanID())
	errored := shells[0]
	require.Equal(t, root.SpanID(), errored.ParentSpanID())
	require.Equal(t, root.TraceID(), errored.TraceID())
	require.Equal(t, ptrace.SpanKindInternal, errored.Kind())
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.Equal(t, pcommon.NewTimestampFromTime(base.Add(2*time.Second-1500*time.Millisecond)), errored.StartTimestamp())
	require.Equal(t, pcommon.NewTimestampFromTime(base.Add(2*time.Second)), errored.EndTimestamp())
	require.Equal(t, "execute_tool", attrString(t, errored, "gen_ai.operation.name"))
	require.Equal(t, "shell", attrString(t, errored, "gen_ai.tool.name"))
	require.Equal(t, "call-1", attrString(t, errored, "gen_ai.tool.call.id"))
	require.Equal(t, "builtin", attrString(t, errored, "gen_ai.tool.type"))
	require.Equal(t, "normalized", attrString(t, errored, "coding_agent.source"))
	require.Equal(t, "cursor", attrString(t, errored, "coding_agent.client.name"))
	require.Equal(t, NameGrokBotToolResult, attrString(t, errored, "coding_agent.source.event"))
	require.Equal(t, ptrace.StatusCodeError, errored.Status().Code())
	require.Equal(t, "tool execution failed", errored.Status().Message())
	for _, raw := range []string{"cursor.tool.name", "cursor.grok_bot.tool_call.id", "cursor.grok_bot.tool_result.outcome",
		"cursor.grok_bot.tool_result.duration_ms", "cursor.grok_bot.tool_result.error_category", "cursor.grok_bot.turn.id"} {
		_, ok := errored.Attributes().Get(raw)
		require.False(t, ok, "%s must not reach canonical output", raw)
	}
	require.Equal(t, ptrace.StatusCodeUnset, shells[1].Status().Code(), "a success re-run has no error status")

	mcp := byName["execute_tool search_issues"][0]
	require.Equal(t, "mcp", attrString(t, mcp, "gen_ai.tool.type"))
	require.Equal(t, "call-2", attrString(t, mcp, "gen_ai.tool.call.id"))
	require.Equal(t, NameGrokBotMCPToolCall, attrString(t, mcp, "coding_agent.source.event"))
	require.Equal(t, pcommon.NewTimestampFromTime(base.Add(3*time.Second-250*time.Millisecond)), mcp.StartTimestamp())
	require.Equal(t, ptrace.StatusCodeUnset, mcp.Status().Code())
	_, ok := mcp.Attributes().Get("cursor.mcp.server.name")
	require.False(t, ok)

	denied := byName["execute_tool read"][0]
	require.Equal(t, denied.StartTimestamp(), denied.EndTimestamp(), "no duration: zero-length span")
	require.Equal(t, ptrace.StatusCodeUnset, denied.Status().Code(), "denied is not a failure")
	_, ok = denied.Attributes().Get("gen_ai.tool.call.id")
	require.False(t, ok, "no call id on the wire, no key")

	failedMCP := byName["execute_tool create_pr"][0]
	require.Equal(t, ptrace.StatusCodeError, failedMCP.Status().Code())

	bare := byName["execute_tool"]
	require.Len(t, bare, 1, "a row without a tool name still produces a span")
	_, ok = bare[0].Attributes().Get("gen_ai.tool.name")
	require.False(t, ok)
}

func TestToolSpanIDsAreDeterministic(t *testing.T) {
	first := spansByName(mustBuildTrace(t, grokBotBurst(), "quiet"))["execute_tool shell"]
	second := spansByName(mustBuildTrace(t, grokBotBurst(), "quiet"))["execute_tool shell"]
	require.Equal(t, first[0].SpanID(), second[0].SpanID())
	require.Equal(t, first[1].SpanID(), second[1].SpanID())
}
```

If `attrString` is not already defined in `trace_test.go`, check `connector_test.go` for `stringAttrOn`; use whichever exists, or add:

```go
func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on %s", key, span.Name())
	return value.Str()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -run 'GrokBot|ToolSpanIDs' -v`
Expected: FAIL to compile (`NameGrokBotToolResult` undefined).

- [ ] **Step 3: Write the code**

In `event.go`, extend the constant block:

```go
	// NameAPIRequest and friends are canonical event names: the wire event
	// name flattened ("cursor.api.request" → "api_request"), which is also
	// the constant the wire puts in the body of every record outside the
	// conversation_content family.
	NameAPIRequest = "api_request"
	NameAPIError   = "api_error"
	// NameGrokBotToolResult and NameGrokBotMCPToolCall are the Grok Bot
	// action rows that record one settled tool call: builtin tools and MCP
	// tools respectively. They become execute_tool spans.
	NameGrokBotToolResult  = "grok_bot_tool_result"
	NameGrokBotMCPToolCall = "grok_bot_mcp_tool_call"
```

and add after `IsCorrectionEvent`:

```go
// IsToolEvent reports whether a canonical event name is one of the Grok Bot
// tool rows the edge turns into execute_tool spans.
func IsToolEvent(name string) bool {
	return name == NameGrokBotToolResult || name == NameGrokBotMCPToolCall
}
```

In `trace.go`, add the import `"math"` and `"time"`, call `appendToolSpans(ss.Spans(), traceID, rootID, events)` in `buildTrace` right after `appendChatSpans(...)`, and add:

```go
// appendToolSpans turns each Grok Bot tool row into an execute_tool child of
// the burst root. The wire records a settled call once per row, so a builtin
// call that re-ran after a transient model-stream failure yields two spans
// sharing a call id; span ids derive from the row's own event id, so they
// never collide. The row's duration sets the span start; a row without one is
// a zero-length span at its timestamp.
func appendToolSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []Event) {
	for _, event := range events {
		if !IsToolEvent(event.Name) {
			continue
		}
		span := spans.AppendEmpty()
		name := "execute_tool"
		if tool := StringValue(event.Attrs["cursor.tool.name"]); tool != "" {
			name += " " + tool
			span.Attributes().PutStr("gen_ai.tool.name", tool)
		}
		span.SetName(name)
		span.SetTraceID(traceID)
		span.SetSpanID(deterministicSpanID(traceID, "tool:"+event.EventID))
		span.SetParentSpanID(parentID)
		span.SetKind(ptrace.SpanKindInternal)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(event.Timestamp.Add(-toolDuration(event))))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
		span.Attributes().PutStr("coding_agent.source", "normalized")
		span.Attributes().PutStr("coding_agent.client.name", "cursor")
		span.Attributes().PutStr("coding_agent.source.event", event.Name)
		if callID := StringValue(event.Attrs["cursor.grok_bot.tool_call.id"]); callID != "" {
			span.Attributes().PutStr("gen_ai.tool.call.id", callID)
		}
		toolType, failed := "builtin", StringValue(event.Attrs["cursor.grok_bot.tool_result.outcome"]) == "error"
		if event.Name == NameGrokBotMCPToolCall {
			toolType, failed = "mcp", StringValue(event.Attrs["cursor.tool.status"]) == "failure"
		}
		span.Attributes().PutStr("gen_ai.tool.type", toolType)
		if failed {
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage("tool execution failed")
		}
	}
}

// toolDuration reads the row's wall time: the builtin and MCP rows spell the
// field differently. A negative or overflowing value reports no duration so a
// span's start can never move past its end.
func toolDuration(event Event) time.Duration {
	key := "cursor.grok_bot.tool_result.duration_ms"
	if event.Name == NameGrokBotMCPToolCall {
		key = "cursor.grok_bot.mcp.duration_ms"
	}
	ms, ok := Int64Value(event.Attrs[key])
	if !ok || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -run 'GrokBot|ToolSpanIDs' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/event.go connector/codingagentconnector/internal/cursor/trace.go connector/codingagentconnector/internal/cursor/trace_test.go
git commit -m "feat(cursor): emit execute_tool spans for Grok Bot tool rows"
```

### Task 2: Tool rows leave the root-event path

**Files:**
- Change: `connector/codingagentconnector/internal/cursor/trace.go` (`appendRootEvents`)
- Test: `connector/codingagentconnector/internal/cursor/trace_test.go`

- [ ] **Step 1: Write the failing test**

Append to `trace_test.go`:

```go
// TestToolRowsAreNotRootEvents pins that a record which became a span does
// not also land on the root as a name-only event.
func TestToolRowsAreNotRootEvents(t *testing.T) {
	root := spansByName(mustBuildTrace(t, grokBotBurst(), "quiet"))["invoke_agent cursor"][0]
	for i := 0; i < root.Events().Len(); i++ {
		name := root.Events().At(i).Name()
		require.False(t, IsToolEvent(name), "tool row %q must not be a root event", name)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -run TestToolRowsAreNotRootEvents -v`
Expected: FAIL (root carries `grok_bot_tool_result` events).

- [ ] **Step 3: Write the code**

In `appendRootEvents`, change the first skip to:

```go
		if event.Name == NameAPIRequest || IsToolEvent(event.Name) {
			continue
		}
```

and update the function comment's first sentence to: "appendRootEvents lands every record neither the chat spans nor the tool spans consumed: " and leave the rest of the sentence unchanged.

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -count=1`
Expected: PASS (the committed fixture has no tool rows yet, so the replay fixture test still matches).

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/trace.go connector/codingagentconnector/internal/cursor/trace_test.go
git commit -m "fix(cursor): keep tool rows off the root event list"
```

### Task 3: Fixture extension and conformance signal

**Files:**
- Change: `connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json`
- Regenerate: `connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json`
- Change: `connector/codingagentconnector/internal/cursor/conformance_test.go`
- Change: `connector/codingagentconnector/conformance_test.go` (cursor fixture signals)
- Test: `connector/codingagentconnector/internal/cursor/fixtures_test.go` (existing replay tests)

- [ ] **Step 1: Add two tool rows to burst A of the native fixture**

Burst A holds the records with `timeUnixNano` below `1787306640000000000` (10:04 UTC). Insert these two records into the `cursor.telemetry` scope's `logRecords` array, after the `hook_execution_complete` record (`1787306425000000000`) and before `cloud_agent_mcp_auth_error` (`1787306430000000000`). The fixture records carry no `eventName`, so these use the body as the constant name the way the others do:

```json
{
  "timeUnixNano": "1787306426000000000",
  "eventName": "cursor.grok_bot.tool_result",
  "body": { "stringValue": "grok_bot_tool_result" },
  "attributes": [
    { "key": "cursor.event.id", "value": { "stringValue": "customer-telemetry:v1:ev-6a" } },
    { "key": "cursor.source_event.id", "value": { "stringValue": "src-6a" } },
    { "key": "cursor.conversation.id", "value": { "stringValue": "11111111-2222-3333-4444-555555555555" } },
    { "key": "cursor.grok_bot.provenance", "value": { "stringValue": "client" } },
    { "key": "cursor.grok_bot.turn.id", "value": { "stringValue": "turn-aaaa" } },
    { "key": "cursor.grok_bot.tool_call.id", "value": { "stringValue": "call_a1" } },
    { "key": "cursor.tool.name", "value": { "stringValue": "shell" } },
    { "key": "cursor.grok_bot.tool_result.outcome", "value": { "stringValue": "error" } },
    { "key": "cursor.grok_bot.tool_result.duration_ms", "value": { "intValue": "1200" } },
    { "key": "cursor.grok_bot.tool_result.error_category", "value": { "stringValue": "timeout" } }
  ]
},
{
  "timeUnixNano": "1787306427000000000",
  "eventName": "cursor.grok_bot.mcp_tool_call",
  "body": { "stringValue": "grok_bot_mcp_tool_call" },
  "attributes": [
    { "key": "cursor.event.id", "value": { "stringValue": "customer-telemetry:v1:ev-6b" } },
    { "key": "cursor.source_event.id", "value": { "stringValue": "src-6b" } },
    { "key": "cursor.conversation.id", "value": { "stringValue": "11111111-2222-3333-4444-555555555555" } },
    { "key": "cursor.grok_bot.provenance", "value": { "stringValue": "client" } },
    { "key": "cursor.grok_bot.turn.id", "value": { "stringValue": "turn-aaaa" } },
    { "key": "cursor.grok_bot.tool_call.id", "value": { "stringValue": "call_a2" } },
    { "key": "cursor.tool.name", "value": { "stringValue": "search_issues" } },
    { "key": "cursor.tool.status", "value": { "stringValue": "success" } },
    { "key": "cursor.mcp.server.name", "value": { "stringValue": "github" } },
    { "key": "cursor.grok_bot.mcp.duration_ms", "value": { "intValue": "300" } }
  ]
}
```

Check that the JSON parses: `python3 -I -c 'import json,sys; json.load(open(sys.argv[1]))' connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json`.

- [ ] **Step 2: See the replay test fail, then regenerate the canonical fixture**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -run TestFixtureReplayMatchesCanonicalFixture -count=1`
Expected: FAIL (two new spans in burst A).

Create a throwaway `connector/codingagentconnector/internal/cursor/regen_test.go`:

```go
package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestRegenerateCanonicalFixture(t *testing.T) {
	emitted := replayFixture(t, false)
	require.Len(t, emitted, 2)
	out, err := (&ptrace.JSONMarshaler{}).MarshalTraces(emitted[0])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "cursor-canonical.otlp.json"), out, 0o644))
}
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -run TestRegenerateCanonicalFixture -count=1`, then `rm connector/codingagentconnector/internal/cursor/regen_test.go`.

Inspect: `python3 -I - <<'EOF'
import json
d=json.load(open('connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json'))
for rs in d['resourceSpans']:
    for ss in rs['scopeSpans']:
        for s in ss['spans']:
            print(s['name'], [a['key'] for a in s['attributes']], s.get('status'))
EOF`

Expected: `execute_tool shell` with `gen_ai.tool.call.id`, `gen_ai.tool.type`, and an error status; `execute_tool search_issues` with status unset; no `cursor.*` key on either; the root's events no longer list the two rows.

- [ ] **Step 3: Run the whole package**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/cursor/ -count=1`
Expected: PASS, including `TestFixtureReplayShuffleStable`.

- [ ] **Step 4: Add the conformance signal**

In `connector/codingagentconnector/internal/cursor/conformance_test.go` and in the `cursor` fixture of `connector/codingagentconnector/conformance_test.go`, append to the signals:

```go
{RawKey: "cursor.tool.name", CanonicalKey: "gen_ai.tool.name", Kind: canonical.Presence},
{RawKey: "cursor.grok_bot.tool_call.id", CanonicalKey: "gen_ai.tool.call.id", Kind: canonical.Presence},
```

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test ./... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json connector/codingagentconnector/internal/cursor/conformance_test.go connector/codingagentconnector/conformance_test.go
git commit -m "test(cursor): cover Grok Bot tool rows in the fixture and conformance signals"
```

### Task 4: Matrix docs

**Files:**
- Change: `docs/harnesses/cursor.md`

- [ ] **Step 1: Event-to-span table**

In the "Event-to-span mapping" table, add after the `api_correction_<kind>, unjoined` row:

```
| `grok_bot_tool_result`, `grok_bot_mcp_tool_call` (`grok_bot` surface, Action Recording on) | `execute_tool <tool>` span |
```

and change the `skill_activated, hook_execution_complete, cloud_agent_*, grok_bot_*` row to read `grok_bot_*` **except the two tool rows**.

- [ ] **Step 2: New attribute table**

After the `##### invoke_agent cursor root` table, add:

```markdown
##### grok_bot_tool_result / grok_bot_mcp_tool_call → execute_tool

Grok Bot is the only surface whose tool calls arrive as log records, and only
with Action Recording on; the IDE, CLI, cloud-agent, and bugbot surfaces
record tool calls as the `cursor.tool.calls` metric only. One row is one
settled call. A builtin call that re-ran after a transient model-stream
failure produces two rows sharing a call id, so two spans.

| Raw key | Canonical key | Status |
|---|---|---|
| `cursor.tool.name` | `gen_ai.tool.name` | mapped (also names the span; a row without it emits as bare `execute_tool`) |
| `cursor.grok_bot.tool_call.id` | `gen_ai.tool.call.id` | mapped (absent when the row carries none; the wire marks it optional on `tool_result`) |
| record kind | `gen_ai.tool.type` | mapped (`builtin` for `tool_result`, `mcp` for `mcp_tool_call`) |
| `cursor.grok_bot.tool_result.outcome` = `error` / `cursor.tool.status` = `failure` | span Status = Error, message `tool execution failed` | mapped to status (`denied`, `cancelled`, and `success` leave the status unset; the attribute itself drops) |
| `cursor.grok_bot.tool_result.duration_ms`, `cursor.grok_bot.mcp.duration_ms` | — | dropped (used for span bounds only: start = timestamp − duration; a row without one is a zero-length span) |
| `cursor.grok_bot.tool_result.error_category`, `…target_host`, `cursor.mcp.server.name`, `cursor.grok_bot.mcp.*`, `cursor.grok_bot.turn.id`, `cursor.grok_bot.provenance`, `cursor.grok_bot.event.sequence`, `cursor.grok_bot.initiated_by` | — | dropped |
```

- [ ] **Step 3: Adjust the "not provided" table and the event-name section**

Replace `| `gen_ai.tool.*` / `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |` with:

```
| `gen_ai.tool.name` / `gen_ai.tool.call.id` / `gen_ai.tool.type` | mapped on the Grok Bot surface only (see the tool table); not provided for every other surface |
| `gen_ai.tool.status` / `gen_ai.agent.id` / `gen_ai.agent.version` | not provided |
```

In the "Connector-written attributes" list, change `gen_ai.operation.name` = `invoke_agent` / `chat` to add `/ `execute_tool``.

- [ ] **Step 4: Record the artifact rename**

In the "Root/chat span events" table, change the `coding_agent.cursor.artifact.{file_name,content_type}` row's status to:

```
dropped (cloud-agent event name survives as `cloud_agent_artifact`; before the wire-event-name rule landed in PR #104 this event was named `cloud_agent_artifact_created` from the record body — the wire documents `cursor.cloud_agent.artifact` with no kind attribute, so there is no suffix to restore, and anything matching on the old name needs the new one)
```

- [ ] **Step 5: Commit**

```bash
git add docs/harnesses/cursor.md
git commit -m "docs(cursor): record Grok Bot tool spans and the artifact event rename"
```

### Task 5: Full check

- [ ] **Step 1: Run the repo check**

Run from the repo root: `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`. Fix anything it reports and amend the owning commit. No live stack exists for Cursor, so nothing else to run.

- [ ] **Step 2: Open the PR**

Push the branch and open a PR titled `feat(cursor): Grok Bot tool spans` with the spec path in the body and a note that the fixture extension is hand-built from the wire reference (checked 2026-10-19).
