# Cursor Native-Log Trace Synthesis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correlate Cursor's native `cursor.telemetry` OTLP log records into canonical `invoke_agent cursor` traces with `chat` point-span children, on a new claiming-router logs edge beside the Codex edge.

**Architecture:** A new stateful `internal/cursor` package mirrors `internal/codex` (event parsing, burst state machine with sweep loop, deterministic trace builder, mdatagen telemetry). The logs-to-traces edge in `factory.go` becomes a router that passes each batch to both edges; claims stay disjoint because Codex claims `codex.`-prefixed event names and Cursor claims scope-prefix `cursor.telemetry`. Validation runs on synthetic fixtures authored from Cursor's published wire reference — no live E2E exists.

**Tech Stack:** Go, OpenTelemetry Collector `pdata`/`connector` APIs (pinned versions already in `connector/codingagentconnector/go.mod`), mdatagen via `./scripts/generate.sh`, testify.

**Spec:** `docs/superpowers/specs/2026-08-21-cursor-logs-design.md`

## Global Constraints

- Run tests in the connector module: `cd connector/codingagentconnector && go test ./...`. Race variant: `go test -race ./...`.
- Run `./scripts/check.sh` from the repo root before pushing (repo rule; covers both modules, lint, mdatagen freshness, build).
- No new Go dependencies. No new configuration fields, component types, or metrics. YAML keys `turn_timeout`, `reorder_window`, `max_active_turns`, `max_events_per_turn` keep their names, defaults, and validation.
- Canonical output copies record fields by **allowlist only**. Never copy prompt, completion, or tool content (the Cursor wire carries none today; the allowlist keeps it that way).
- Cursor spans never set `gen_ai.provider.name` and never set `coding_agent.turn.complete`.
- Attribute destinations stay in the documented namespaces: `gen_ai.*`, `coding_agent.*` (vendor specifics under `coding_agent.cursor.*`).
- Conventional-commit subjects, lowercase, imperative (repo style: `feat(cursor): ...`, `test(e2e): ...`, `docs: ...`).
- The Vale prose-lint hook runs on every docs edit; keep prose active-voiced or it blocks the write.
- `docs/harnesses.md` has an unrelated uncommitted edit in the working tree — never stage, revert, or "clean" it.
- All timestamps in fixtures use fixed RFC3339 nanoseconds so span output stays byte-deterministic.

---

### Task 1: Parse and claim Cursor log records

**Files:**
- Create: `connector/codingagentconnector/internal/cursor/event.go`
- Test: `connector/codingagentconnector/internal/cursor/event_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces (package `cursor`, used by Tasks 2–4):
  - `type Event struct` with fields `Body, EventID, ConversationID, UsageEventID string`, `Timestamp time.Time`, `Attrs map[string]any`, `Resource map[string]any`
  - `func ParseRecord(record plog.LogRecord, scopeName string, resource pcommon.Resource) (Event, bool)`
  - Body constants: `BodyAPIRequest = "api_request"`, `BodyAPIError = "api_error"`, `BodySkillActivated = "skill_activated"`, `BodyHookExecutionComplete = "hook_execution_complete"`
  - `func IsCorrectionBody(body string) bool`, `func IsCloudAgentBody(body string) bool`
  - Coercion helpers `StringValue(any) string`, `Int64Value(any) (int64, bool)`, `BoolValue(any) (bool, bool)` — unexported copies of the codex ones (codex's are package-private; per-package helpers match the existing style)

- [ ] **Step 1: Write the failing tests**

`event_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func testResource() pcommon.Resource {
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "cursor")
	resource.Attributes().PutStr("service.version", "1.16.5")
	resource.Attributes().PutInt("cursor.team.id", 4242)
	resource.Attributes().PutStr("cursor.surface", "cli")
	resource.Attributes().PutStr("cursor.entrypoint", "cli")
	return resource
}

func apiRequestRecord(ts time.Time, attrs map[string]any) plog.LogRecord {
	record := plog.NewLogRecord()
	record.Body().SetStr(BodyAPIRequest)
	record.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	// FromRaw replaces the whole map, so merge overrides into one map first.
	merged := map[string]any{
		"cursor.event.id":                          "customer-telemetry:v1:ev-1",
		"cursor.source_event.id":                   "src-1",
		"cursor.conversation.id":                   "11111111-2222-3333-4444-555555555555",
		"cursor.usage_event.id":                    "ue-1",
		"cursor.model.name":                        "claude-4.5-sonnet",
		"cursor.api.request.input_tokens":          100,
		"cursor.api.request.output_tokens":         200,
		"cursor.api.request.cache_read_tokens":     50,
		"cursor.api.request.cache_creation_tokens": 10,
	}
	for k, v := range attrs {
		merged[k] = v
	}
	record.Attributes().FromRaw(merged)
	return record
}

func TestParseRecordClaimsCursorScope(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	event, ok := ParseRecord(apiRequestRecord(ts, nil), "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, BodyAPIRequest, event.Body)
	require.Equal(t, "customer-telemetry:v1:ev-1", event.EventID)
	require.Equal(t, "11111111-2222-3333-4444-555555555555", event.ConversationID)
	require.Equal(t, "ue-1", event.UsageEventID)
	require.Equal(t, ts, event.Timestamp)
	require.Equal(t, "cursor", event.Resource["service.name"])
	require.Equal(t, int64(100), event.Attrs["cursor.api.request.input_tokens"])
}

func TestParseRecordToleratesScopeVersions(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	_, ok := ParseRecord(apiRequestRecord(ts, nil), "cursor.telemetry/0.2.0", testResource())
	require.True(t, ok)
}

func TestParseRecordRejectsForeignScope(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	_, ok := ParseRecord(apiRequestRecord(ts, nil), "codex_cli_rs", testResource())
	require.False(t, ok)
	_, ok = ParseRecord(apiRequestRecord(ts, nil), "other.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordRejectsMissingConversationID(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().Remove("cursor.conversation.id")
	_, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordSkipsCodexNamedRecords(t *testing.T) {
	// The claiming guard from the spec: never claim a record whose event.name
	// belongs to the Codex edge, whatever scope carries it.
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().PutStr("event.name", "codex.user_prompt")
	_, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordFallsBackToObservedTimestamp(t *testing.T) {
	observed := time.Date(2026, 8, 21, 10, 0, 5, 0, time.UTC)
	record := apiRequestRecord(time.Time{}, nil)
	record.SetTimestamp(0)
	record.SetObservedTimestamp(pcommon.NewTimestampFromTime(observed))
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, observed, event.Timestamp)
}

func TestParseRecordMarksInferredTimestamp(t *testing.T) {
	record := apiRequestRecord(time.Time{}, nil)
	record.SetTimestamp(0)
	record.SetObservedTimestamp(0)
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.False(t, event.Timestamp.IsZero())
	require.Equal(t, true, event.Attrs["coding_agent.timestamp.inferred"])
}

func TestParseRecordDerivesEventIDWhenAbsent(t *testing.T) {
	// The wire guarantees cursor.event.id, but a malformed record must not
	// dedupe against every other id-less record (the empty-string key would).
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().Remove("cursor.event.id")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.NotEmpty(t, event.EventID)

	other := apiRequestRecord(ts.Add(time.Second), nil)
	other.Attributes().Remove("cursor.event.id")
	event2, ok := ParseRecord(other, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.NotEqual(t, event.EventID, event2.EventID)
}

func TestParseRecordCoercesTokenStringsAndFloats(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, map[string]any{
		"cursor.api.request.input_tokens": "300",
	})
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	value, ok := Int64Value(event.Attrs["cursor.api.request.input_tokens"])
	require.True(t, ok)
	require.Equal(t, int64(300), value)
}

func TestBodyClassification(t *testing.T) {
	require.True(t, IsCorrectionBody("api_correction_not_billed_errored"))
	require.False(t, IsCorrectionBody("api_request"))
	require.True(t, IsCloudAgentBody("cloud_agent_setup_completed"))
	require.False(t, IsCloudAgentBody("hook_execution_complete"))
}

func TestParseRecordKeepsUnknownBody(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetStr("future_event_kind")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "future_event_kind", event.Body)
}

func TestParseRecordHandlesNonStringBody(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetEmptyMap().PutStr("kind", "api_request")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "", event.Body)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/cursor/ -run TestParseRecord -v`
Expected: FAIL — package has no `event.go` yet (compile errors naming `ParseRecord`, `Event`, body constants).

- [ ] **Step 3: Write `event.go`**

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

const (
	// cursorScopePrefix claims records by instrumentation scope. The wire
	// reference documents scope `cursor.telemetry`/0.1.0`; prefix matching
	// tolerates version-suffixed or renamed-future scope names.
	cursorScopePrefix = "cursor.telemetry"

	attrEventID      = "cursor.event.id"
	attrConversation = "cursor.conversation.id"
	attrUsageEventID = "cursor.usage_event.id"

	// BodyAPIRequest and friends name the OTLP log-record bodies from the
	// wire reference. Bodies are unprefixed ("api_request", not
	// "cursor.api.request"); the cursor.* namespace carries attributes only.
	BodyAPIRequest            = "api_request"
	BodyAPIError              = "api_error"
	BodySkillActivated        = "skill_activated"
	BodyHookExecutionComplete = "hook_execution_complete"
)

// Event is one claimed Cursor log record with the correlation keys the wire
// guarantees or the connector requires. Attrs carries the raw record
// attributes; span construction copies from it by allowlist only.
type Event struct {
	Body           string
	EventID        string
	ConversationID string
	UsageEventID   string
	Timestamp      time.Time
	Attrs          map[string]any
	Resource       map[string]any
}

// IsCorrectionBody reports whether a body names an api.correction record
// ("api_correction_<kind>" per the wire reference).
func IsCorrectionBody(body string) bool {
	return strings.HasPrefix(body, "api_correction_")
}

// IsCloudAgentBody reports whether a body belongs to the cloud_agents family.
func IsCloudAgentBody(body string) bool {
	return strings.HasPrefix(body, "cloud_agent_")
}

// ParseRecord claims a record for the Cursor edge when its instrumentation
// scope matches and it carries a conversation id. It declines codex-named
// records so the two logs edges stay disjoint whatever a payload contains.
func ParseRecord(record plog.LogRecord, scopeName string, resource pcommon.Resource) (Event, bool) {
	if !strings.HasPrefix(scopeName, cursorScopePrefix) {
		return Event{}, false
	}
	attrs := record.Attributes().AsRaw()
	if strings.HasPrefix(StringValue(attrs["event.name"]), "codex.") {
		return Event{}, false
	}
	conversationID := StringValue(attrs[attrConversation])
	if conversationID == "" {
		return Event{}, false
	}

	var body string
	if record.Body().Type() == pcommon.ValueTypeStr {
		body = record.Body().Str()
	}

	var ts time.Time
	if record.Timestamp() != 0 {
		ts = record.Timestamp().AsTime()
	} else if record.ObservedTimestamp() != 0 {
		ts = record.ObservedTimestamp().AsTime()
	} else {
		ts = time.Now()
		attrs["coding_agent.timestamp.inferred"] = true
	}

	return Event{
		Body:           body,
		EventID:        eventID(body, ts, attrs),
		ConversationID: conversationID,
		UsageEventID:   StringValue(attrs[attrUsageEventID]),
		Timestamp:      ts,
		Attrs:          attrs,
		Resource:       resource.Attributes().AsRaw(),
	}, true
}

// eventID returns the wire dedupe key, or derives a stable one for malformed
// records missing it: dedupe on the raw empty string would collapse every
// id-less record into one another.
func eventID(body string, ts time.Time, attrs map[string]any) string {
	if id := StringValue(attrs[attrEventID]); id != "" {
		return id
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	_, _ = h.Write([]byte(body))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatInt(ts.UnixNano(), 10)))
	for _, key := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{'='})
		_, _ = h.Write([]byte(StringValue(attrs[key])))
	}
	return fmt.Sprintf("derived:%x", h.Sum(nil))
}

func StringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func Int64Value(v any) (int64, bool) {
	switch value := v.(type) {
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint64:
		if value <= 1<<62 {
			return int64(value), true
		}
	case float64:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func BoolValue(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(value)
		return parsed, err == nil
	}
	return false, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test ./internal/cursor/ -v`
Expected: PASS for all `TestParseRecord*`, `TestBodyClassification` tests.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/event.go connector/codingagentconnector/internal/cursor/event_test.go
git commit -m "feat(cursor): parse and claim cursor.telemetry log records"
```

---

### Task 2: Burst state machine and connector lifecycle

**Files:**
- Create: `connector/codingagentconnector/internal/cursor/connector.go`
- Create: `connector/codingagentconnector/internal/cursor/metrics.go`
- Create: `connector/codingagentconnector/internal/cursor/leak_test.go`
- Test: `connector/codingagentconnector/internal/cursor/connector_test.go`
- Edit: `connector/codingagentconnector/internal/cursor/trace.go` — placeholder stub only (Task 3 replaces it): `func buildTrace(b *burstState, reason, scopeVersion string) ptrace.Traces { return ptrace.NewTraces() }`

**Interfaces:**
- Consumes: `Event`, `ParseRecord` from Task 1; `codex.Config` (the public `Config` alias) for the four knobs.
- Produces (used by Tasks 3–5):
  - `func New(cfg *codex.Config, set connector.Settings, next consumer.Traces) (connector.Logs, error)`
  - `type burstState struct` with fields `conversationID string`, `events []Event`, `seen map[string]struct{}`, `resource map[string]any`, `first, last, lastSeen time.Time`, `truncated bool`
  - `func buildTrace(burst *burstState, reason, scopeVersion string) ptrace.Traces` (stub now, real in Task 3)
  - Connector field `bursts map[string]*burstState` (tests inject state directly)

- [ ] **Step 1: Write the failing tests**

Test helpers first (top of `connector_test.go`), then the behavior tests. The helpers build real `plog.Logs` so every test goes through `ParseRecord`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

type testRecord struct {
	body  string
	ts    time.Time
	attrs map[string]any
}

const testConversation = "11111111-2222-3333-4444-555555555555"

func baseAttrs(eventID string) map[string]any {
	return map[string]any{
		"cursor.event.id":        eventID,
		"cursor.source_event.id": "src-" + eventID,
		"cursor.conversation.id": testConversation,
	}
}

func makeCursorLogs(records ...testRecord) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "cursor")
	rl.Resource().Attributes().PutStr("cursor.surface", "cli")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("cursor.telemetry")
	sl.Scope().SetVersion("0.1.0")
	for _, rec := range records {
		attrs := baseAttrs("ev-" + rec.body + "-" + rec.ts.Format(time.RFC3339Nano))
		for k, v := range rec.attrs {
			attrs[k] = v
		}
		record := sl.LogRecords().AppendEmpty()
		record.Body().SetStr(rec.body)
		record.SetTimestamp(pcommon.NewTimestampFromTime(rec.ts))
		record.Attributes().FromRaw(attrs)
	}
	return logs
}

func apiRequest(ts time.Time, eventID, model string, in, out int64) testRecord {
	attrs := map[string]any{
		"cursor.usage_event.id":                 "ue-" + eventID,
		"cursor.api.request.input_tokens":       in,
		"cursor.api.request.output_tokens":      out,
		"cursor.api.request.cache_read_tokens":  int64(0),
		"cursor.api.request.cache_creation_tokens": int64(0),
	}
	if model != "" {
		attrs["cursor.model.name"] = model
	}
	return testRecord{body: BodyAPIRequest, ts: ts, attrs: attrs}
}

type traceSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, traces)
	return nil
}

func (s *traceSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.traces)
}

func testSettings() connector.Settings {
	return connector.Settings{
		TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()},
	}
}
```

Behavior tests (same file):

```go
func TestConsumeLogsOpensOneBurstPerConversation(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "claude-4.5-sonnet", 10, 20),
		apiRequest(base.Add(2*time.Second), "b", "claude-4.5-sonnet", 5, 5),
	)))
	require.Len(t, instance.bursts, 1)
	require.Len(t, instance.bursts[testConversation].events, 2)
}

func TestDedupeDropsRedeliveredRecords(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background()) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	batch := makeCursorLogs(apiRequest(base, "a", "gpt-5.2", 10, 20))
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch))
	// At-least-once redelivery of the same batch must not double-count.
	require.NoError(t, instance.ConsumeLogs(context.Background(), batch))
	require.Len(t, instance.bursts[testConversation].events, 1)
}

func TestQuietWindowClosesBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	sink := &traceSink{}
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	require.NoError(t, connectorLogs.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, connectorLogs.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "gpt-5.2", 10, 20),
	)))
	require.Eventually(t, func() bool { return sink.count() == 1 }, time.Second, 5*time.Millisecond)
}

func TestTimeoutClosesLongBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	cfg.TurnTimeout = time.Second
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	require.NoError(t, instance.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	// Direct state injection (the codex connector tests use the same
	// technique): a burst old enough to hit turn_timeout while lastSeen stays
	// too recent for the quiet window, proving timeout wins the ordering.
	now := time.Now()
	instance.bursts[testConversation] = &burstState{
		conversationID: testConversation,
		events:         []Event{{Body: BodyAPIRequest, EventID: "ev-x", ConversationID: testConversation, Timestamp: now.Add(-2 * cfg.TurnTimeout)}},
		seen:           map[string]struct{}{"ev-x": {}},
		first:          now.Add(-2 * cfg.TurnTimeout),
		last:           now.Add(-2 * cfg.TurnTimeout),
		lastSeen:       now.Add(-cfg.ReorderWindow),
	}
	finalized := instance.collectReady(now)
	require.Len(t, finalized, 1)
	require.Equal(t, "timeout", finalized[0].reason)
}

func TestQuietBeatsNothingWhenBurstIdle(t *testing.T) {
	// Only ReorderWindow shrinks: with the default 10m turn_timeout the
	// burst is far too young for timeout, so quiet is the only ready reason.
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 5 * time.Millisecond
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	now := time.Now()
	instance.bursts[testConversation] = &burstState{
		conversationID: testConversation,
		events:         []Event{{Body: BodyAPIRequest, EventID: "ev-x", ConversationID: testConversation, Timestamp: now.Add(-time.Second)}},
		seen:           map[string]struct{}{"ev-x": {}},
		first:          now.Add(-time.Second),
		last:           now.Add(-time.Second),
		lastSeen:       now.Add(-time.Second),
	}
	finalized := instance.collectReady(now)
	require.Len(t, finalized, 1)
	require.Equal(t, "quiet", finalized[0].reason)
}

func TestResumedConversationOpensNewBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "gpt-5.2", 10, 20),
	)))
	// Simulate the quiet close having happened, then a much later record.
	require.NoError(t, instance.flushAll(context.Background(), "quiet"))
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base.Add(time.Hour), "b", "gpt-5.2", 1, 2),
	)))
	require.Len(t, instance.bursts, 1)
	require.Equal(t, base.Add(time.Hour), instance.bursts[testConversation].first)
}

func TestEvictionBoundsActiveBursts(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.MaxActiveTurns = 1
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	first := makeCursorLogs(apiRequest(base, "a", "m", 1, 1))
	first.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr("cursor.conversation.id", "conv-first")
	second := makeCursorLogs(apiRequest(base.Add(time.Second), "b", "m", 1, 1))
	second.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().PutStr("cursor.conversation.id", "conv-second")
	require.NoError(t, instance.ConsumeLogs(context.Background(), first))
	require.NoError(t, instance.ConsumeLogs(context.Background(), second))
	require.Len(t, instance.bursts, 1)
	require.Contains(t, instance.bursts, "conv-second")
}

func TestTruncationSetsMarker(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	cfg.MaxEvents = 1
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, instance.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
		apiRequest(base.Add(time.Second), "b", "m", 1, 1),
	)))
	require.True(t, instance.bursts[testConversation].truncated)
	require.Len(t, instance.bursts[testConversation].events, 1)
}

func TestIgnoresForeignAndScopelessRecords(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	connectorLogs, err := New(cfg, testSettings(), &traceSink{})
	require.NoError(t, err)
	instance := connectorLogs.(*cursorConnector)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	foreign := rl.ScopeLogs().AppendEmpty()
	foreign.Scope().SetName("some.other.scope")
	record := foreign.LogRecords().AppendEmpty()
	record.Body().SetStr(BodyAPIRequest)
	record.Attributes().PutStr("cursor.conversation.id", testConversation)
	require.NoError(t, instance.ConsumeLogs(context.Background(), logs))
	require.Empty(t, instance.bursts)
}

func TestShutdownFlushesOpenBurst(t *testing.T) {
	cfg := codex.NewDefaultConfig()
	sink := &traceSink{}
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), makeCursorLogs(
		apiRequest(base, "a", "m", 1, 1),
	)))
	require.NoError(t, connectorLogs.Shutdown(context.Background()))
	require.Equal(t, 1, sink.count())
}
```

`leak_test.go` (mirror of `internal/codex/leak_test.go`):

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
```

Note: codex's `leak_test.go` owns `TestMain` for its package; `internal/cursor` is a separate package, so its own `TestMain` compiles independently.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/cursor/ -v`
Expected: FAIL — `New`, `cursorConnector`, `burstState`, `collectReady`, `flushAll` undefined (compile errors).

- [ ] **Step 3: Write `metrics.go` and `connector.go`**

`metrics.go` (same instruments as codex, its own TelemetryBuilder instance):

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadata"
)

// telemetry adapts the shared mdatagen instruments to the cursor edge, with
// the same lossy paths as the codex edge: bursts closed by reason, redelivered
// records dropped by dedupe, and truncated bursts.
type telemetry struct {
	builder *metadata.TelemetryBuilder
}

func (t *telemetry) recordEmitted(ctx context.Context, reason string, truncated bool) {
	t.builder.CodingAgentTurnsEmitted.Add(ctx, 1, metric.WithAttributes(attribute.String("finish_reason", reason)))
	if truncated {
		t.builder.CodingAgentTurnsTruncated.Add(ctx, 1)
	}
}

func (t *telemetry) recordDroppedRecords(ctx context.Context, n int64) {
	if n > 0 {
		t.builder.CodingAgentEventsDropped.Add(ctx, n)
	}
}
```

`connector.go` (mirrors `internal/codex/connector.go` with Cursor semantics — note the two deliberate differences called out in comments):

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadata"
)

// finalizedBurst pairs a burst removed from active state with the reason it
// closed, so the emit path carries them together.
type finalizedBurst struct {
	burst  *burstState
	reason string
}

type burstState struct {
	conversationID string
	events         []Event
	seen           map[string]struct{}
	resource       map[string]any
	first          time.Time
	last           time.Time
	lastSeen       time.Time
	truncated      bool
}

type cursorConnector struct {
	config       *codex.Config
	set          connector.Settings
	next         consumer.Traces
	scopeVersion string

	mu     sync.Mutex
	bursts map[string]*burstState
	stop   chan struct{}
	done   chan struct{}

	started  atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once

	telemetry *telemetry
}

// New creates the stateful Cursor logs-to-traces edge. It shares the
// provider-neutral Config alias with the codex edge.
func New(cfg *codex.Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	ts := set.TelemetrySettings
	if ts.MeterProvider == nil {
		ts.MeterProvider = noopmetric.NewMeterProvider()
	}
	builder, err := metadata.NewTelemetryBuilder(ts)
	if err != nil {
		return nil, err
	}
	scopeVersion := set.BuildInfo.Version
	if scopeVersion == "" {
		scopeVersion = "0.1.0"
	}
	c := &cursorConnector{
		config: cfg, set: set, next: next, scopeVersion: scopeVersion,
		bursts: make(map[string]*burstState),
		stop:   make(chan struct{}), done: make(chan struct{}),
		telemetry: &telemetry{builder: builder},
	}
	// The active-turns gauge is shared with the codex edge; the provider
	// attribute keeps the two async observations from colliding on one
	// timeseries.
	if err := builder.RegisterCodingAgentActiveTurnsCallback(func(_ context.Context, observer metric.Int64Observer) error {
		observer.Observe(c.activeBurstCount(), metric.WithAttributes(attribute.String("provider", "cursor")))
		return nil
	}); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cursorConnector) activeBurstCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.bursts))
}

func (*cursorConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *cursorConnector) Start(context.Context, component.Host) error {
	c.startOnce.Do(func() {
		c.started.Store(true)
		go c.sweepLoop()
	})
	return nil
}

func (c *cursorConnector) Shutdown(ctx context.Context) error {
	c.telemetry.builder.Shutdown()
	c.stopOnce.Do(func() { close(c.stop) })
	if c.started.Load() {
		select {
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.flushAll(ctx, "shutdown")
}

func (c *cursorConnector) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	events := make([]Event, 0, logs.LogRecordCount())
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		rl := logs.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			scopeName := sl.Scope().Name()
			for k := 0; k < sl.LogRecords().Len(); k++ {
				if event, ok := ParseRecord(sl.LogRecords().At(k), scopeName, rl.Resource()); ok {
					events = append(events, event)
				}
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	var finalized []finalizedBurst
	var dropped int64
	now := time.Now()
	c.mu.Lock()
	for _, event := range events {
		key := event.ConversationID
		burst := c.bursts[key]
		if burst != nil {
			if _, duplicate := burst.seen[event.EventID]; duplicate {
				dropped++
				continue
			}
		}
		if burst == nil {
			if len(c.bursts) >= c.config.MaxActiveTurns {
				finalized = append(finalized, finalizedBurst{burst: c.evictOldestLocked(), reason: "evicted"})
			}
			burst = &burstState{
				conversationID: key,
				first:          event.Timestamp,
				last:           event.Timestamp,
				lastSeen:       now,
				resource:       event.Resource,
			}
			c.bursts[key] = burst
		}
		burst.add(event, now, c.config.MaxEvents)
	}
	c.mu.Unlock()
	c.telemetry.recordDroppedRecords(ctx, dropped)
	return c.emit(ctx, finalized)
}

func (b *burstState) add(event Event, now time.Time, maxEvents int) {
	if event.Timestamp.Before(b.first) {
		b.first = event.Timestamp
	}
	if event.Timestamp.After(b.last) {
		b.last = event.Timestamp
	}
	b.lastSeen = now
	if len(b.events) < maxEvents {
		b.events = append(b.events, event)
		if b.seen == nil {
			b.seen = make(map[string]struct{})
		}
		b.seen[event.EventID] = struct{}{}
	} else {
		b.truncated = true
	}
}

func (c *cursorConnector) sweepLoop() {
	interval := c.config.ReorderWindow / 2
	if interval <= 0 {
		interval = 10 * time.Millisecond
	} else if interval > time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer func() { ticker.Stop(); close(c.done) }()
	for {
		select {
		case now := <-ticker.C:
			if err := c.emit(context.Background(), c.collectReady(now)); err != nil {
				c.set.Logger.Error("failed to emit reconstructed cursor trace", zap.Error(err))
			}
		case <-c.stop:
			return
		}
	}
}

// collectReady closes bursts on two clocks. The timeout check runs first and
// measures from the burst's first event: a burst that keeps receiving records
// never goes quiet, so turn_timeout is the only cap on its growth. The quiet
// check measures arrival silence since the last record and is the normal
// close, because the Cursor wire has no completion event.
func (c *cursorConnector) collectReady(now time.Time) []finalizedBurst {
	c.mu.Lock()
	defer c.mu.Unlock()
	var finalized []finalizedBurst
	for key, burst := range c.bursts {
		reason := ""
		if now.Sub(burst.first) >= c.config.TurnTimeout {
			reason = "timeout"
		} else if now.Sub(burst.lastSeen) >= c.config.ReorderWindow {
			reason = "quiet"
		}
		if reason != "" {
			delete(c.bursts, key)
			finalized = append(finalized, finalizedBurst{burst: burst, reason: reason})
		}
	}
	return finalized
}

func (c *cursorConnector) evictOldestLocked() *burstState {
	var oldestKey string
	var oldest *burstState
	for key, burst := range c.bursts {
		if oldest == nil || burst.lastSeen.Before(oldest.lastSeen) {
			oldestKey, oldest = key, burst
		}
	}
	delete(c.bursts, oldestKey)
	return oldest
}

func (c *cursorConnector) emit(ctx context.Context, finalized []finalizedBurst) error {
	// Continue past a failing burst so one transient downstream error during a
	// drain does not abandon the bursts already removed from active state.
	var errs error
	for _, fb := range finalized {
		if fb.burst == nil {
			continue
		}
		if err := c.next.ConsumeTraces(ctx, buildTrace(fb.burst, fb.reason, c.scopeVersion)); err != nil {
			errs = errors.Join(errs, err)
		}
		c.telemetry.recordEmitted(ctx, fb.reason, fb.burst.truncated)
	}
	return errs
}

func (c *cursorConnector) flushAll(ctx context.Context, reason string) error {
	c.mu.Lock()
	finalized := make([]finalizedBurst, 0, len(c.bursts))
	for key, burst := range c.bursts {
		finalized = append(finalized, finalizedBurst{burst: burst, reason: reason})
		delete(c.bursts, key)
	}
	c.mu.Unlock()
	return c.emit(ctx, finalized)
}

var _ connector.Logs = (*cursorConnector)(nil)
```

Also create `trace.go` with only the stub for now:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// buildTrace is implemented in full by the trace-construction task; the
// state-machine tests only need a callable that returns a valid batch.
func buildTrace(*burstState, string, string) ptrace.Traces {
	return ptrace.NewTraces()
}
```

- [ ] **Step 4: Run tests to verify they pass (with race)**

Run: `cd connector/codingagentconnector && go test -race ./internal/cursor/ -v`
Expected: PASS for all tests including goleak.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/connector.go connector/codingagentconnector/internal/cursor/metrics.go connector/codingagentconnector/internal/cursor/leak_test.go connector/codingagentconnector/internal/cursor/connector_test.go connector/codingagentconnector/internal/cursor/trace.go
git commit -m "feat(cursor): correlate cursor records into activity bursts"
```

---

### Task 3: Canonical trace construction

**Files:**
- Edit: `connector/codingagentconnector/internal/cursor/trace.go` (replace the stub)
- Test: `connector/codingagentconnector/internal/cursor/trace_test.go`

**Interfaces:**
- Consumes: `burstState`, `Event`, body constants, coercion helpers.
- Produces: `buildTrace(*burstState, string, string) ptrace.Traces` — the real implementation used by Task 2's emit path and Task 4's replay test.

- [ ] **Step 1: Write the failing tests**

`trace_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func burstForTest() *burstState {
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Body: BodyAPIRequest, EventID: "ev-1", ConversationID: testConversation, UsageEventID: "ue-1",
			Timestamp: base, Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.model.name": "claude-4.5-sonnet",
				"cursor.api.request.input_tokens": int64(100), "cursor.api.request.output_tokens": int64(200),
				"cursor.api.request.cache_read_tokens": int64(50), "cursor.api.request.cache_creation_tokens": int64(10),
				"cursor.api.billable": true,
			}},
		{Body: BodyAPIRequest, EventID: "ev-2", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(2 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{
				"cursor.api.request.input_tokens": int64(7), "cursor.api.request.output_tokens": int64(9),
				"cursor.api.request.cache_read_tokens": int64(0), "cursor.api.request.cache_creation_tokens": int64(0),
			}},
		{Body: BodyAPIError, EventID: "ev-3", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(3 * time.Second), Resource: testResourceRaw(), Attrs: map[string]any{}},
		{Body: "api_correction_not_billed_errored", EventID: "ev-4", ConversationID: testConversation, UsageEventID: "ue-2",
			Timestamp: base.Add(4 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.api.correction.kind": "not_billed_errored"}},
		{Body: BodySkillActivated, EventID: "ev-5", ConversationID: testConversation,
			Timestamp: base.Add(5 * time.Second), Resource: testResourceRaw(),
			Attrs: map[string]any{"cursor.skill.name": "code-review", "cursor.skill.trigger": "agent_read", "cursor.skill.source": "user"}},
	}
	return &burstState{
		conversationID: testConversation,
		events:         events,
		seen:           map[string]struct{}{},
		resource:       testResourceRaw(),
		first:          base, last: base.Add(5 * time.Second), lastSeen: base.Add(5 * time.Second),
	}
}

func testResourceRaw() map[string]any {
	return map[string]any{
		"service.name": "cursor", "service.version": "1.16.5",
		"cursor.team.id": int64(4242), "cursor.surface": "cli", "cursor.entrypoint": "cli", "cursor.user.id": int64(99),
	}
}

func spansByName(traces ptrace.Traces) map[string][]ptrace.Span {
	out := map[string][]ptrace.Span{}
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		ss := traces.ResourceSpans().At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			spans := ss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				out[span.Name()] = append(out[span.Name()], span)
			}
		}
	}
	return out
}

func TestBuildTraceRoot(t *testing.T) {
	traces := buildTrace(burstForTest(), "quiet", "0.1.0")
	roots := spansByName(traces)["invoke_agent cursor"]
	require.Len(t, roots, 1)
	root := roots[0]
	require.Equal(t, "invoke_agent", stringAttrOn(t, root, "gen_ai.operation.name"))
	require.Equal(t, "cursor", stringAttrOn(t, root, "gen_ai.agent.name"))
	require.Equal(t, testConversation, stringAttrOn(t, root, "gen_ai.conversation.id"))
	require.Equal(t, "cursor", stringAttrOn(t, root, "coding_agent.client.name"))
	require.Equal(t, "1.16.5", stringAttrOn(t, root, "coding_agent.client.version"))
	require.Equal(t, "normalized", stringAttrOn(t, root, "coding_agent.source"))
	require.Equal(t, "quiet", stringAttrOn(t, root, "coding_agent.turn.finish_reason"))
	require.Equal(t, "cli", stringAttrOn(t, root, "coding_agent.cursor.surface"))
	require.Equal(t, "cli", stringAttrOn(t, root, "coding_agent.cursor.entrypoint"))
	require.Equal(t, int64(4242), intAttrOn(t, root, "coding_agent.cursor.team.id"))
	require.Equal(t, int64(99), intAttrOn(t, root, "coding_agent.cursor.user.id"))
	// The connector never claims completion and never guesses a provider.
	_, ok := root.Attributes().Get("coding_agent.turn.complete")
	require.False(t, ok)
	_, ok = root.Attributes().Get("gen_ai.provider.name")
	require.False(t, ok)
	// Usage rollup sums both api_request records.
	require.Equal(t, int64(107), intAttrOn(t, root, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(209), intAttrOn(t, root, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(50), intAttrOn(t, root, "gen_ai.usage.cache_read.input_tokens"))
	require.Equal(t, int64(10), intAttrOn(t, root, "gen_ai.usage.cache_creation.input_tokens"))
	// Bounds cover first..last event timestamps.
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	require.Equal(t, base, root.StartTimestamp().AsTime())
	require.Equal(t, base.Add(5*time.Second), root.EndTimestamp().AsTime())
}

func TestBuildTraceChatSpans(t *testing.T) {
	traces := buildTrace(burstForTest(), "quiet", "0.1.0")
	chats := spansByName(traces)["chat claude-4.5-sonnet"]
	require.Len(t, chats, 1)
	chat := chats[0]
	require.Equal(t, "chat", stringAttrOn(t, chat, "gen_ai.operation.name"))
	require.Equal(t, "claude-4.5-sonnet", stringAttrOn(t, chat, "gen_ai.request.model"))
	require.Equal(t, BodyAPIRequest, stringAttrOn(t, chat, "coding_agent.source.event"))
	require.Equal(t, int64(100), intAttrOn(t, chat, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(200), intAttrOn(t, chat, "gen_ai.usage.output_tokens"))
	require.Equal(t, true, boolAttrOn(t, chat, "coding_agent.cursor.billable"))
	// Point span: the wire reports tokens at request grain with no timing.
	require.Equal(t, chat.StartTimestamp(), chat.EndTimestamp())
	require.Equal(t, rootOf(t, traces).SpanID(), chat.ParentSpanID())
	require.Equal(t, rootOf(t, traces).TraceID(), chat.TraceID())
}

func TestBuildTraceModellessChatKeepsBareName(t *testing.T) {
	traces := buildTrace(burstForTest(), "quiet", "0.1.0")
	chats := spansByName(traces)["chat"]
	require.Len(t, chats, 1)
	_, ok := chats[0].Attributes().Get("gen_ai.request.model")
	require.False(t, ok)
}

func TestBuildTraceErrorJoinAndCorrectionJoin(t *testing.T) {
	traces := buildTrace(burstForTest(), "quiet", "0.1.0")
	// ev-2 (ue-2) has an api_error and a correction; both attach there.
	chat := spansByName(traces)["chat"][0]
	require.Equal(t, ptrace.StatusCodeError, chat.Status().Code)
	require.Len(t, chat.Events(), 1)
	require.Equal(t, "api_correction_not_billed_errored", chat.Events().At(0).Name())
	require.Equal(t, "not_billed_errored", chat.Events().At(0).Attributes().AsRaw()["coding_agent.cursor.correction.kind"])
	require.Equal(t, "ue-2", chat.Events().At(0).Attributes().AsRaw()["coding_agent.cursor.usage_event_id"])
	// The unjoined api_error/correction never land on the root in this burst.
	root := rootOf(t, traces)
	for i := 0; i < root.Events().Len(); i++ {
		name := root.Events().At(i).Name()
		require.NotEqual(t, BodyAPIError, name)
		require.False(t, IsCorrectionBody(name))
	}
}

func TestBuildTraceRootEvents(t *testing.T) {
	traces := buildTrace(burstForTest(), "quiet", "0.1.0")
	root := rootOf(t, traces)
	require.Equal(t, 1, root.Events().Len())
	event := root.Events().At(0)
	require.Equal(t, BodySkillActivated, event.Name())
	require.Equal(t, "code-review", event.Attributes().AsRaw()["coding_agent.cursor.skill.name"])
	require.Equal(t, "agent_read", event.Attributes().AsRaw()["coding_agent.cursor.skill.trigger"])
	require.Equal(t, "user", event.Attributes().AsRaw()["coding_agent.cursor.skill.source"])
	require.Equal(t, "ev-5", event.Attributes().AsRaw()["coding_agent.cursor.event_id"])
}

func TestBuildTraceUnjoinedErrorAndCorrectionLandOnRoot(t *testing.T) {
	burst := burstForTest()
	// Drop the api_request carrying ue-2 so its error and correction have no
	// join target inside the burst.
	burst.events = append(burst.events[:1], burst.events[2:]...)
	traces := buildTrace(burst, "quiet", "0.1.0")
	root := rootOf(t, traces)
	var names []string
	for i := 0; i < root.Events().Len(); i++ {
		names = append(names, root.Events().At(i).Name())
	}
	require.Contains(t, names, BodyAPIError)
	require.Contains(t, names, "api_correction_not_billed_errored")
}

func TestBuildTraceTimeoutSetsErrorStatus(t *testing.T) {
	traces := buildTrace(burstForTest(), "timeout", "0.1.0")
	require.Equal(t, ptrace.StatusCodeError, rootOf(t, traces).Status().Code)
}

func TestBuildTraceDeterministicIDs(t *testing.T) {
	first := buildTrace(burstForTest(), "quiet", "0.1.0")
	second := buildTrace(burstForTest(), "quiet", "0.1.0")
	require.Equal(t, rootOf(t, first).TraceID(), rootOf(t, second).TraceID())
	require.Equal(t, rootOf(t, first).SpanID(), rootOf(t, second).SpanID())

	// A different first event id changes the trace id.
	burst := burstForTest()
	burst.events[0].EventID = "ev-other"
	third := buildTrace(burst, "quiet", "0.1.0")
	require.NotEqual(t, rootOf(t, first).TraceID(), rootOf(t, third).TraceID())
}

func TestBuildTraceCopiesNothingOutsideAllowlist(t *testing.T) {
	burst := burstForTest()
	burst.events = append(burst.events, Event{
		Body: "future_event_kind", EventID: "ev-9", ConversationID: testConversation,
		Timestamp: burst.last.Add(time.Second), Resource: testResourceRaw(),
		Attrs: map[string]any{
			"cursor.event.id": "ev-9", "cursor.conversation.id": testConversation,
			"cursor.some.new.field": "prompt-like content that must not propagate",
		},
	})
	traces := buildTrace(burst, "quiet", "0.1.0")
	spans := spansByName(traces)
	for _, group := range spans {
		for _, span := range group {
			_, ok := span.Attributes().Get("cursor.some.new.field")
			require.False(t, ok, "span %s leaked a non-allowlisted attribute", span.Name())
		}
	}
	root := rootOf(t, traces)
	var generic bool
	for i := 0; i < root.Events().Len(); i++ {
		raw := root.Events().At(i).Attributes().AsRaw()
		_, leaked := raw["cursor.some.new.field"]
		require.False(t, leaked)
		if root.Events().At(i).Name() == "future_event_kind" {
			generic = true
			require.Len(t, raw, 1) // only the event id
		}
	}
	require.True(t, generic)
}

func stringAttrOn(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Str()
}

func intAttrOn(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Int()
}

func boolAttrOn(t *testing.T, span ptrace.Span, key string) bool {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %s missing on %s", key, span.Name())
	return value.Bool()
}

func rootOf(t *testing.T, traces ptrace.Traces) ptrace.Span {
	t.Helper()
	roots := spansByName(traces)["invoke_agent cursor"]
	require.Len(t, roots, 1)
	return roots[0]
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/cursor/ -run TestBuildTrace -v`
Expected: FAIL — root/chat spans absent (stub returns empty traces).

- [ ] **Step 3: Write the real `trace.go`**

Replace the stub file entirely:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"crypto/sha256"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const instrumentationScope = "github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector"

// chatTokenAttrs maps the api_request token ints to their canonical
// destinations; it drives both per-span usage and the root rollup.
var chatTokenAttrs = []struct{ source, dest string }{
	{"cursor.api.request.input_tokens", "gen_ai.usage.input_tokens"},
	{"cursor.api.request.output_tokens", "gen_ai.usage.output_tokens"},
	{"cursor.api.request.cache_read_tokens", "gen_ai.usage.cache_read.input_tokens"},
	{"cursor.api.request.cache_creation_tokens", "gen_ai.usage.cache_creation.input_tokens"},
}

type attrMapping struct{ source, dest string }

var rootEventAllowlist = map[string][]attrMapping{
	BodySkillActivated: {
		{"cursor.skill.name", "coding_agent.cursor.skill.name"},
		{"cursor.skill.trigger", "coding_agent.cursor.skill.trigger"},
		{"cursor.skill.source", "coding_agent.cursor.skill.source"},
		{"cursor.plugin.name", "coding_agent.cursor.plugin.name"},
	},
	BodyHookExecutionComplete: {
		{"cursor.hook.name", "coding_agent.cursor.hook.name"},
		{"cursor.hook.type", "coding_agent.cursor.hook.type"},
		{"cursor.hook.outcome", "coding_agent.cursor.hook.outcome"},
		{"cursor.hook.duration_ms", "coding_agent.cursor.hook.duration_ms"},
		{"cursor.plugin.name", "coding_agent.cursor.plugin.name"},
	},
}

// cloudAgentAllowlist covers the cloud_agents family; bodies are prefixed so
// one table serves all of them.
var cloudAgentAllowlist = []attrMapping{
	{"cursor.cloud_agent.pull_request.kind", "coding_agent.cursor.pull_request.kind"},
	{"cursor.cloud_agent.pull_request.number", "coding_agent.cursor.pull_request.number"},
	{"cursor.cloud_agent.pull_request.draft", "coding_agent.cursor.pull_request.draft"},
	{"cursor.cloud_agent.setup.kind", "coding_agent.cursor.setup.kind"},
	{"cursor.cloud_agent.setup.duration_ms", "coding_agent.cursor.setup.duration_ms"},
	{"cursor.cloud_agent.setup.reason", "coding_agent.cursor.setup.reason"},
	{"cursor.cloud_agent.artifact.file_name", "coding_agent.cursor.artifact.file_name"},
	{"cursor.cloud_agent.artifact.content_type", "coding_agent.cursor.artifact.content_type"},
	{"cursor.mcp.server.name", "coding_agent.cursor.mcp.server.name"},
}

func buildTrace(burst *burstState, reason, scopeVersion string) ptrace.Traces {
	events := append([]Event(nil), burst.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	traceID := deterministicTraceID(burst.conversationID, events)
	rootID := deterministicSpanID(traceID, "root")

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	_ = rs.Resource().Attributes().FromRaw(burst.resource)
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(instrumentationScope)
	ss.Scope().SetVersion(scopeVersion)

	root := ss.Spans().AppendEmpty()
	root.SetName("invoke_agent cursor")
	root.SetTraceID(traceID)
	root.SetSpanID(rootID)
	root.SetKind(ptrace.SpanKindInternal)
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(burst.first))
	root.SetEndTimestamp(pcommon.NewTimestampFromTime(burst.last))
	putRootAttributes(root.Attributes(), burst, events, reason)
	if reason == "timeout" {
		root.Status().SetCode(ptrace.StatusCodeError)
		root.Status().SetMessage("burst closed after turn_timeout")
	}

	appendChatSpans(ss.Spans(), traceID, rootID, events)
	appendRootEvents(root.Events(), events)
	return traces
}

func putRootAttributes(attrs pcommon.Map, burst *burstState, events []Event, reason string) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "cursor")
	attrs.PutStr("gen_ai.conversation.id", burst.conversationID)
	attrs.PutStr("coding_agent.client.name", "cursor")
	if len(events) > 0 {
		attrs.PutStr("coding_agent.source.event", events[0].Body)
	}
	attrs.PutStr("coding_agent.turn.finish_reason", reason)
	attrs.PutBool("coding_agent.turn.events_truncated", burst.truncated)
	attrs.PutStr("coding_agent.source", "normalized")
	// Deliberately no gen_ai.provider.name: the wire never names the upstream
	// provider and the connector does not guess one. Deliberately no
	// coding_agent.turn.complete: quiet closing cannot distinguish a finished
	// model turn from an abandoned one.
	copyStringAttr(attrs, burst.resource, "service.version", "coding_agent.client.version")
	copyStringAttr(attrs, burst.resource, "cursor.surface", "coding_agent.cursor.surface")
	copyStringAttr(attrs, burst.resource, "cursor.entrypoint", "coding_agent.cursor.entrypoint")
	copyIntAttr(attrs, burst.resource, "cursor.team.id", "coding_agent.cursor.team.id")
	copyIntAttr(attrs, burst.resource, "cursor.user.id", "coding_agent.cursor.user.id")
	putAggregateUsage(attrs, events)
}

func appendChatSpans(spans ptrace.SpanSlice, traceID pcommon.TraceID, parentID pcommon.SpanID, events []Event) {
	errored := map[string]bool{}
	corrections := map[string][]Event{}
	for _, event := range events {
		if event.UsageEventID == "" {
			continue
		}
		if event.Body == BodyAPIError {
			errored[event.UsageEventID] = true
		} else if IsCorrectionBody(event.Body) {
			corrections[event.UsageEventID] = append(corrections[event.UsageEventID], event)
		}
	}
	for _, event := range events {
		if event.Body != BodyAPIRequest {
			continue
		}
		span := spans.AppendEmpty()
		name := "chat"
		if model := StringValue(event.Attrs["cursor.model.name"]); model != "" {
			name += " " + model
			span.Attributes().PutStr("gen_ai.request.model", model)
		}
		span.SetName(name)
		span.SetTraceID(traceID)
		span.SetSpanID(deterministicSpanID(traceID, "chat:"+event.EventID))
		span.SetParentSpanID(parentID)
		span.SetKind(ptrace.SpanKindClient)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("coding_agent.source.event", event.Body)
		for _, m := range chatTokenAttrs {
			copyIntAttr(span.Attributes(), event.Attrs, m.source, m.dest)
		}
		copyBoolAttr(span.Attributes(), event.Attrs, "cursor.api.billable", "coding_agent.cursor.billable")
		if event.UsageEventID != "" {
			if errored[event.UsageEventID] {
				span.Status().SetCode(ptrace.StatusCodeError)
				span.Status().SetMessage("model request errored")
			}
			for _, correction := range corrections[event.UsageEventID] {
				e := span.Events().AppendEmpty()
				e.SetName(correction.Body)
				e.SetTimestamp(pcommon.NewTimestampFromTime(correction.Timestamp))
				copyStringAttr(e.Attributes(), correction.Attrs, "cursor.api.correction.kind", "coding_agent.cursor.correction.kind")
				e.Attributes().PutStr("coding_agent.cursor.usage_event_id", event.UsageEventID)
			}
		}
	}
}

Root events skip records the chat spans already consumed, and api errors or corrections whose `usage_event.id` joins an in-burst `api_request` (they attach to that span instead):

```go
func appendRootEvents(dst ptrace.SpanEventSlice, events []Event) {
	joined := map[string]bool{}
	for _, event := range events {
		if event.Body == BodyAPIRequest && event.UsageEventID != "" {
			joined[event.UsageEventID] = true
		}
	}
	for _, event := range events {
		if event.Body == BodyAPIRequest {
			continue
		}
		if (event.Body == BodyAPIError || IsCorrectionBody(event.Body)) && event.UsageEventID != "" && joined[event.UsageEventID] {
			continue
		}
		e := dst.AppendEmpty()
		e.SetName(event.Body)
		e.SetTimestamp(pcommon.NewTimestampFromTime(event.Timestamp))
		e.Attributes().PutStr("coding_agent.cursor.event_id", event.EventID)
		switch {
		case event.Body == BodyAPIError:
			copyStringAttr(e.Attributes(), event.Attrs, "cursor.model.name", "coding_agent.cursor.model")
			copyStringAttr(e.Attributes(), event.Attrs, attrUsageEventID, "coding_agent.cursor.usage_event_id")
		case IsCorrectionBody(event.Body):
			copyStringAttr(e.Attributes(), event.Attrs, "cursor.api.correction.kind", "coding_agent.cursor.correction.kind")
			copyStringAttr(e.Attributes(), event.Attrs, attrUsageEventID, "coding_agent.cursor.usage_event_id")
		default:
			mappings := rootEventAllowlist[event.Body]
			if IsCloudAgentBody(event.Body) {
				mappings = cloudAgentAllowlist
			}
			for _, m := range mappings {
				copyStringAttr(e.Attributes(), event.Attrs, m.source, m.dest)
				copyIntAttr(e.Attributes(), event.Attrs, m.source, m.dest)
				copyBoolAttr(e.Attributes(), event.Attrs, m.source, m.dest)
			}
		}
	}
}

func deterministicTraceID(conversationID string, events []Event) pcommon.TraceID {
	firstID := ""
	if len(events) > 0 {
		firstID = events[0].EventID
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"cursor", conversationID, firstID}, "\x00")))
	var id pcommon.TraceID
	copy(id[:], sum[:16])
	return id
}

func deterministicSpanID(traceID pcommon.TraceID, discriminator string) pcommon.SpanID {
	h := sha256.New()
	_, _ = h.Write(traceID[:])
	_, _ = h.Write([]byte(discriminator))
	sum := h.Sum(nil)
	var id pcommon.SpanID
	copy(id[:], sum[:8])
	return id
}

func putAggregateUsage(attrs pcommon.Map, events []Event) {
	totals := map[string]int64{}
	seen := map[string]bool{}
	for _, event := range events {
		if event.Body != BodyAPIRequest {
			continue
		}
		for _, m := range chatTokenAttrs {
			if value, ok := Int64Value(event.Attrs[m.source]); ok {
				totals[m.source] += value
				seen[m.source] = true
			}
		}
	}
	for _, m := range chatTokenAttrs {
		if seen[m.source] {
			attrs.PutInt(m.dest, totals[m.source])
		}
	}
}

func copyIntAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := Int64Value(src[from]); ok {
		dst.PutInt(to, value)
	}
}

func copyStringAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value := StringValue(src[from]); value != "" {
		dst.PutStr(to, value)
	}
}

func copyBoolAttr(dst pcommon.Map, src map[string]any, from, to string) {
	if value, ok := BoolValue(src[from]); ok {
		dst.PutBool(to, value)
	}
}
```

(Assemble the final `trace.go` from these blocks: constants and tables, `buildTrace`, `putRootAttributes`, `appendChatSpans`, `appendRootEvents`, the ID helpers, `putAggregateUsage`, and the three copy helpers.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test -race ./internal/cursor/ -v`
Expected: PASS — all Task 2 and Task 3 tests, no leaks.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/trace.go connector/codingagentconnector/internal/cursor/trace_test.go
git commit -m "feat(cursor): build canonical traces from cursor bursts"
```

---

### Task 4: Wire-reference fixtures and replay test

**Files:**
- Create: `connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json`
- Create: `connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json`
- Test: `connector/codingagentconnector/internal/cursor/fixtures_test.go`

**Interfaces:**
- Consumes: `New` (Task 2), `buildTrace` (Task 3).
- Produces: the two fixture files; Task 6 reads `cursor-canonical.otlp.json` by relative path.

- [ ] **Step 1: Author the raw fixture**

`testdata/cursor-native-logs.json` — OTLP/JSON logs export (`plog.JSONMarshaler` shape), one resource, one scope `cursor.telemetry/0.1.0`, two bursts of the same conversation separated by a 90-second source-timestamp gap, plus the join cases. Use exactly these records (timestamps fixed; resource: `service.name=cursor`, `service.version=1.16.5`, `cursor.team.id=4242`, `cursor.surface=cli`, `cursor.entrypoint=cli`, `cursor.user.id=99`):

Burst A (conversation `11111111-2222-3333-4444-555555555555`):

| # | body | ts (2026-08-21T…) | attrs |
|---|------|-------------------|-------|
| 1 | `api_request` | 10:00:00Z | event.id `…:ev-1`, usage_event.id `ue-1`, model `claude-4.5-sonnet`, tokens in/out/cache_read/cache_creation 100/200/50/10, billable true |
| 2 | `skill_activated` | 10:00:01Z | event.id `…:ev-2`, skill.name `code-review`, trigger `agent_read`, source `user` |
| 3 | `api_request` | 10:00:20Z | event.id `…:ev-3`, usage_event.id `ue-2`, tokens 7/9/0/0 (no model) |
| 4 | `api_error` | 10:00:21Z | event.id `…:ev-4`, usage_event.id `ue-2`, model `claude-4.5-sonnet` |
| 5 | `api_correction_not_billed_errored` | 10:00:22Z | event.id `…:ev-5`, usage_event.id `ue-2`, correction.kind `not_billed_errored` |
| 6 | `hook_execution_complete` | 10:00:25Z | event.id `…:ev-6`, hook.name `lint`, hook.type `post_tool_use`, hook.outcome `success`, duration_ms 42 |

Burst B (same conversation, later session segment):

| # | body | ts | attrs |
|---|------|----|-------|
| 7 | `api_request` | 10:05:00Z | event.id `…:ev-7`, usage_event.id `ue-3`, model `gpt-5.2`, tokens 30/40/0/0 |
| 8 | `cloud_agent_setup_completed` | 10:05:02Z | event.id `…:ev-8`, setup.kind `completed`, setup.duration_ms 1500 |

Also include one foreign record (scope `some.other.scope`, body `api_request`) and one conversation-less cursor-scope record (`plugin_installed`, no `cursor.conversation.id`) that the connector must ignore — they assert the claiming rules end to end.

Every record also carries `cursor.source_event.id` (`src-1` … `src-8`) per the wire. Event ids use the documented stable prefix shape `customer-telemetry:v1:ev-N`.

Write the file by hand as OTLP/JSON (`resourceLogs[0].scopeLogs[0].logRecords[]`, each record with `body`: `{"stringValue": "api_request"}`, `timeUnixNano`, `attributes` map with `stringValue`/`intValue`/`boolValue` wrappers).

- [ ] **Step 2: Write the replay test**

`fixtures_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
)

func loadFixtureLogs(t *testing.T) plog.Logs {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cursor-native-logs.json"))
	require.NoError(t, err)
	unmarshaler := &plog.JSONUnmarshaler{}
	logs, err := unmarshaler.UnmarshalLogs(raw)
	require.NoError(t, err)
	return logs
}

// replayFixture feeds the fixture through a live connector: burst A's records,
// a wall-clock gap past the quiet window, then burst B's records. The source
// timestamps are fixed, so emitted spans are byte-deterministic even though
// finalization rides the real clock.
func replayFixture(t *testing.T, shuffle bool) []ptrace.Traces {
	t.Helper()
	logs := loadFixtureLogs(t)
	sink := &traceSink{}
	cfg := codex.NewDefaultConfig()
	cfg.ReorderWindow = 20 * time.Millisecond
	cfg.TurnTimeout = time.Minute
	connectorLogs, err := New(cfg, testSettings(), sink)
	require.NoError(t, err)
	require.NoError(t, connectorLogs.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, connectorLogs.Shutdown(context.Background())) })

	split := func() (plog.Logs, plog.Logs) {
		all := loadFixtureLogs(t)
		records := all.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
		first := plog.NewLogs()
		second := plog.NewLogs()
		for _, dst := range []*plog.Logs{&first, &second} {
			rl := dst.ResourceLogs().AppendEmpty()
			all.ResourceLogs().At(0).Resource().CopyTo(rl.Resource())
			sl := rl.ScopeLogs().AppendEmpty()
			sl.Scope().SetName("cursor.telemetry")
			sl.Scope().SetVersion("0.1.0")
		}
		for i := 0; i < records.Len(); i++ {
			record := records.At(i)
			ts := time.Unix(0, int64(record.Timestamp()))
			burstB := ts.After(time.Date(2026, 8, 21, 10, 4, 0, 0, time.UTC))
			if burstB {
				record.CopyTo(second.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().AppendEmpty())
			} else {
				record.CopyTo(first.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().AppendEmpty())
			}
		}
		return first, second
	}
	first, second := split()
	feed := func(batch plog.Logs) {
		if shuffle {
			records := batch.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
			rand.Shuffle(records.Len(), func(i, j int) {
				source := records.At(i)
				dest := records.At(j)
				tmp := plog.NewLogRecord()
				source.CopyTo(tmp)
				dest.CopyTo(source)
				tmp.CopyTo(dest)
			})
		}
		require.NoError(t, connectorLogs.ConsumeLogs(context.Background(), batch))
	}
	feed(first)
	time.Sleep(60 * time.Millisecond) // past reorder_window; burst A closes quiet
	feed(second)
	require.NoError(t, connectorLogs.Shutdown(context.Background())) // burst B closes shutdown
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.traces
}

func TestFixtureReplayMatchesCanonicalFixture(t *testing.T) {
	emitted := replayFixture(t, false)
	require.Len(t, emitted, 2)

	marshaler := &ptrace.JSONMarshaler{}
	actual, err := marshaler.MarshalTraces(emitted[0])
	require.NoError(t, err)
	expected, err := os.ReadFile(filepath.Join("testdata", "cursor-canonical.otlp.json"))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

func TestFixtureReplayShuffleStable(t *testing.T) {
	plain := replayFixture(t, false)
	shuffled := replayFixture(t, true)
	require.Len(t, plain, 2)
	require.Len(t, shuffled, 2)
	marshaler := &ptrace.JSONMarshaler{}
	a, err := marshaler.MarshalTraces(plain[0])
	require.NoError(t, err)
	b, err := marshaler.MarshalTraces(shuffled[0])
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}
```

- [ ] **Step 3: Generate the canonical fixture from the connector's own output**

Temporarily run:

```bash
cd connector/codingagentconnector && go test ./internal/cursor/ -run TestFixtureReplayMatchesCanonicalFixture -v
```

Expected: FAIL (canonical fixture missing). Write a one-off scratch test (do not commit it) that dumps `marshaler.MarshalTraces(emitted[0])` to `testdata/cursor-canonical.otlp.json`, run it, then delete the scratch test and eyeball the generated file against the spec: root `invoke_agent cursor` with `finish_reason=quiet`, two chat spans (one `chat claude-4.5-sonnet` errored with a correction event, one bare `chat`), skill and hook root events, usage rollup 107/209/50/10, no content keys, no provider, no complete marker.

- [ ] **Step 4: Run the fixture tests to verify they pass**

Run: `cd connector/codingagentconnector && go test ./internal/cursor/ -v`
Expected: PASS — replay matches the canonical fixture, and shuffled order derives identical output.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/testdata/cursor-native-logs.json connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json connector/codingagentconnector/internal/cursor/fixtures_test.go
git commit -m "test(cursor): add wire-reference fixtures and replay test"
```

---

### Task 5: Logs-edge claiming router and shared-gauge split

**Files:**
- Create: `connector/codingagentconnector/logs.go`
- Create: `connector/codingagentconnector/logs_test.go`
- Edit: `connector/codingagentconnector/factory.go:29-36` (`createLogsToTraces` returns the router)
- Edit: `connector/codingagentconnector/internal/codex/connector.go:88-93` (gauge callback gains the `provider=codex` attribute)
- Edit: `connector/codingagentconnector/internal/codex/metrics_test.go` (`gaugeValue` takes attributes; the active-turns assertion filters on `provider=codex`)
- Edit: `connector/codingagentconnector/metadata.yaml` (metric descriptions lose Codex-specific wording)
- Regenerate: mdatagen output via `./scripts/generate.sh`

**Interfaces:**
- Consumes: `codex.New`, `cursor.New`.
- Produces: `newLogsRouter(cfg *Config, set connector.Settings, next consumer.Traces) (connector.Logs, error)` — the factory's logs-to-traces constructor. Router `Start`/`Shutdown` fan out to both edges (Codex drain behavior must survive the router).

- [ ] **Step 1: Write the failing router tests**

`logs_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type logsSink struct {
	traces []ptrace.Traces
}

func (*logsSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (s *logsSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.traces = append(s.traces, traces)
	return nil
}

func mixedLogs(t *testing.T) plog.Logs {
	t.Helper()
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()

	codexScope := rl.ScopeLogs().AppendEmpty()
	codexScope.Scope().SetName("codex_cli_rs")
	codexRecord := codexScope.LogRecords().AppendEmpty()
	codexRecord.Attributes().PutStr("event.name", "codex.user_prompt")
	codexRecord.Attributes().PutStr("conversation.id", "codex-conv-1")
	codexRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)))

	cursorScope := rl.ScopeLogs().AppendEmpty()
	cursorScope.Scope().SetName("cursor.telemetry")
	cursorRecord := cursorScope.LogRecords().AppendEmpty()
	cursorRecord.Body().SetStr("api_request")
	cursorRecord.Attributes().PutStr("cursor.event.id", "ev-1")
	cursorRecord.Attributes().PutStr("cursor.conversation.id", "cursor-conv-1")
	cursorRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 8, 21, 10, 0, 1, 0, time.UTC)))

	foreignScope := rl.ScopeLogs().AppendEmpty()
	foreignScope.Scope().SetName("unrelated")
	foreignRecord := foreignScope.LogRecords().AppendEmpty()
	foreignRecord.Body().SetStr("something_else")
	return logs
}

func TestLogsRouterClaimsDisjointSources(t *testing.T) {
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.Start(context.Background(), nil))
	t.Cleanup(func() { require.NoError(t, logsEdge.Shutdown(context.Background())) })

	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), mixedLogs(t)))
	// Shutdown drains both open states; both edges emitted exactly one trace
	// and the foreign record produced none.
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Len(t, sink.traces, 2)
	var rootNames []string
	for _, traces := range sink.traces {
		spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
		require.Equal(t, 1, spans.Len())
		rootNames = append(rootNames, spans.At(0).Name())
	}
	require.Contains(t, rootNames, "invoke_agent codex")
	require.Contains(t, rootNames, "invoke_agent cursor")
}

func TestLogsRouterShutdownSweepsBothEdges(t *testing.T) {
	// A router that embeds no-op StartFunc/ShutdownFunc (like tracesRouter)
	// would silently drop both edges' sweep loops and drain; this test pins
	// the fan-out by requiring shutdown-flushed traces from both sources.
	sink := &logsSink{}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(createDefaultConfig(), set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), mixedLogs(t)))
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Len(t, sink.traces, 2)
}
```

Also add to `internal/codex/metrics_test.go` — the provider-tag assertion (modifying `gaugeValue` to filter on attributes like `counterValue` does):

```go
func gaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	want := attribute.NewSet(attrs...)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "metric %s is not an int64 gauge", name)
			require.NotEmpty(t, g.DataPoints)
			for _, dp := range g.DataPoints {
				if dp.Attributes.Equals(&want) {
					return dp.Value
				}
			}
			t.Fatalf("gauge %s has no datapoint for %v", name, want)
		}
	}
	t.Fatalf("gauge %s not found", name)
	return 0
}
```

and in `TestTelemetryReportsActiveTurns`:

```go
require.Equal(t, int64(2), gaugeValue(t, rm, "otelcol_coding_agent_active_turns", attribute.String("provider", "codex")))
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test . ./internal/codex/ -run 'TestLogsRouter|TestTelemetryReportsActiveTurns' -v`
Expected: FAIL — `newLogsRouter` undefined; codex gauge assertion fails against the untagged series.

- [ ] **Step 3: Add the router, factory change, and codex gauge tag**

`logs.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/cursor"
)

// logsRouter fans the logs-to-traces edge across the provider claimers. Each
// edge ignores foreign records (Codex claims codex.-prefixed event names,
// Cursor claims the cursor.telemetry scope), so a record lands in at most one
// edge and unclaimed records stay out of the canonical edge. Unlike the
// stateless tracesRouter, Start and Shutdown fan out: both edges own sweep
// loops and drain-on-shutdown state.
type logsRouter struct {
	edges []connector.Logs
}

func newLogsRouter(cfg *Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	codexEdge, err := codex.New(cfg, set, next)
	if err != nil {
		return nil, err
	}
	cursorEdge, err := cursor.New(cfg, set, next)
	if err != nil {
		return nil, err
	}
	return &logsRouter{edges: []connector.Logs{codexEdge, cursorEdge}}, nil
}

func (*logsRouter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (r *logsRouter) Start(ctx context.Context, host component.Host) error {
	for _, edge := range r.edges {
		if err := edge.Start(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func (r *logsRouter) Shutdown(ctx context.Context) error {
	var errs error
	for _, edge := range r.edges {
		errs = errors.Join(errs, edge.Shutdown(ctx))
	}
	return errs
}

func (r *logsRouter) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	for _, edge := range r.edges {
		if err := edge.ConsumeLogs(ctx, logs); err != nil {
			return err
		}
	}
	return nil
}

var _ connector.Logs = (*logsRouter)(nil)
```

`factory.go` — replace `createLogsToTraces`:

```go
func createLogsToTraces(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Logs, error) {
	return newLogsRouter(cfg.(*Config), set, next)
}
```

`internal/codex/connector.go` — the gauge observation gains the provider tag:

```go
if err := builder.RegisterCodingAgentActiveTurnsCallback(func(_ context.Context, observer metric.Int64Observer) error {
	observer.Observe(c.activeTurnCount(), metric.WithAttributes(attribute.String("provider", "codex")))
	return nil
}); err != nil {
```

(add `"go.opentelemetry.io/otel/attribute"` to imports if missing).

`metadata.yaml` — replace the two Codex-specific descriptions:

```yaml
    coding_agent_active_turns:
      enabled: true
      stability: development
      description: Coding-agent turns currently held in correlation state, by provider edge.
      unit: "1"
      gauge:
        value_type: int
        async: true
    coding_agent_events_dropped:
      enabled: true
      stability: development
      description: Redelivered coding-agent events dropped by within-burst deduplication.
      unit: "1"
      sum:
        value_type: int
        monotonic: true
```

Regenerate mdatagen artifacts:

```bash
./scripts/generate.sh
```

Expected: `internal/metadata/generated_telemetry.go` and `documentation.md` pick up the new descriptions; no other diffs.

- [ ] **Step 4: Run the full connector test suite with race**

Run: `cd connector/codingagentconnector && go test -race ./...`
Expected: PASS — router tests, codex suite with the retagged gauge, cursor suite, factory/config/generated tests all green (mdatagen freshness is also covered here and later by `check.sh`).

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/logs.go connector/codingagentconnector/logs_test.go connector/codingagentconnector/factory.go connector/codingagentconnector/internal/codex/connector.go connector/codingagentconnector/internal/codex/metrics_test.go connector/codingagentconnector/metadata.yaml connector/codingagentconnector/internal/metadata connector/codingagentconnector/documentation.md
git commit -m "feat: route logs edge across codex and cursor claimers"
```

---

### Task 6: E2E validator assertions for the Cursor canonical shape

**Files:**
- Edit: `e2e/validator/validator.go`
- Test: `e2e/validator/validator_test.go` (repo-root module)

**Interfaces:**
- Consumes: `connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json` (by relative path — the e2e module does not import the connector module).
- Produces: `validateCursorCanonicalFile(path string) error` and unexported helpers, following the existing `validateStrandsRawFile` pattern.

- [ ] **Step 1: Write the failing test**

Append to `e2e/validator/validator_test.go` (follow the file's existing test style and imports):

```go
func TestCursorCanonicalFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "connector", "codingagentconnector", "internal", "cursor", "testdata", "cursor-canonical.otlp.json")
	require.NoError(t, validateCursorCanonicalFile(path))
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./e2e/validator/ -run TestCursorCanonicalFixtureValidates -v`
Expected: FAIL — `validateCursorCanonicalFile` undefined.

- [ ] **Step 3: Add the validators**

In `validator.go`, next to the Strands helpers:

```go
func validateCursorCanonicalFile(path string) error {
	return validateTraceFile(path, "", "cursor", validateCursorCanonicalTraces)
}

func validateCursorCanonicalTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	if err := validateCursorSpans(spans); err != nil {
		return err
	}
	return rejectSensitiveAttrs(spans)
}

func validateCursorSpans(spans []ptrace.Span) error {
	var roots, chats int
	for _, span := range spans {
		switch {
		case strings.HasPrefix(span.Name(), "invoke_agent cursor"):
			roots++
			if err := validateCursorRoot(span); err != nil {
				return err
			}
		case span.Name() == "chat" || strings.HasPrefix(span.Name(), "chat "):
			chats++
			if err := validateCursorChat(span); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected span %q in cursor canonical output", span.Name())
		}
	}
	if roots == 0 {
		return fmt.Errorf("no invoke_agent cursor root found")
	}
	if chats == 0 {
		return fmt.Errorf("no chat spans found under cursor root")
	}
	return nil
}

func validateCursorRoot(span ptrace.Span) error {
	if got := stringAttr(span, "gen_ai.conversation.id"); got == "" {
		return fmt.Errorf("cursor root missing gen_ai.conversation.id")
	}
	if got := stringAttr(span, "coding_agent.turn.finish_reason"); got == "" {
		return fmt.Errorf("cursor root missing finish reason")
	}
	if _, ok := span.Attributes().Get("gen_ai.provider.name"); ok {
		return fmt.Errorf("cursor root must not claim gen_ai.provider.name")
	}
	if _, ok := span.Attributes().Get("coding_agent.turn.complete"); ok {
		return fmt.Errorf("cursor root must not claim completion")
	}
	return nil
}

func validateCursorChat(span ptrace.Span) error {
	if got := stringAttr(span, "gen_ai.operation.name"); got != "chat" {
		return fmt.Errorf("cursor chat span operation %q", got)
	}
	if span.StartTimestamp() != span.EndTimestamp() {
		return fmt.Errorf("cursor chat span must stay a point span; the wire carries no durations")
	}
	return nil
}
```

Add `allSpans(traces ptrace.Traces) []ptrace.Span` if the file lacks such a helper: gather every span from all resource and scope groups, mirroring how `collectRunSpans` iterates but without the run-id filter. Reuse the existing `stringAttr` helper.

- [ ] **Step 4: Run the validator tests**

Run: `go test ./e2e/validator/ -v`
Expected: PASS — new test plus the existing suite.

- [ ] **Step 5: Commit**

```bash
git add e2e/validator/validator.go e2e/validator/validator_test.go
git commit -m "test(e2e): validate cursor canonical fixture shape"
```

---

### Task 7: Documentation and full check

**Files:**
- Edit: `README.md` (root)
- Edit: `connector/codingagentconnector/README.md`
- Edit: `docs/design.md`

**Interfaces:**
- Consumes: everything landed in Tasks 1–6.
- Produces: documentation matching the shipped behavior; nothing downstream.

- [ ] **Step 1: Root README**

In the sources list near the top, add after the Codex bullet:

```markdown
- **Cursor:** correlates native `cursor.telemetry` OTLP logs (Enterprise
  beta, metrics + logs only) into one canonical trace per activity burst,
  keyed on `cursor.conversation.id`. Chat spans carry per-request token
  usage; the wire reports tool calls only as metrics without correlation
  IDs, so canonical traces have no `execute_tool` children.
```

In the "Collector configuration" section, update the paragraph about the logs edge to state that one `coding_agent` instance on the logs pipeline now claims both Codex and Cursor records, and Cursor export arrives server-side from Team Settings (OTLP/HTTP to `/v1/logs`; see the [Cursor OpenTelemetry Export docs](https://cursor.com/docs/enterprise/opentelemetry-export)). Add the wire-reference link to References.

- [ ] **Step 2: Connector README**

Mirror the root README change in the connector-level source list, one short paragraph, same links.

- [ ] **Step 3: `docs/design.md`**

Three edits:

1. Research basis: add a short Cursor paragraph (Enterprise beta, metrics+logs, scope `cursor.telemetry`, `cursor.event.id` dedupe key, no ordering guarantee, no content on the wire; link the wire reference) and note the date `2026-08-21`.
2. Add a "Cursor correlation model" section after the Codex one, covering: burst grain and why (no prompt/completion events), quiet/timeout/shutdown/evicted closure with timeout measured from burst start, `cursor.event.id` dedupe, deterministic IDs from (provider marker, conversation id, first event id) and the partial-replay fragment limitation, point-span chats, the `usage_event.id` joins for errors and corrections, the attribute allowlist, and the absent `execute_tool` children.
3. Future work: rewrite the Cursor bullet to — implemented as native-log synthesis with fixture-based validation; a live Cursor E2E stays blocked on Enterprise-only server-side configuration; tool-call children would need Cursor to log tool calls with a conversation id. Also add Cursor to the "Implemented sources" list.

- [ ] **Step 4: Run the full unpaid CI surface**

Run: `./scripts/check.sh`
Expected: every stage passes (gofmt, shellcheck, golangci-lint both modules, mdatagen freshness, vet, tests + race both modules, collector build, config validation, compose checks, image builds, goreleaser check). Fix anything this surfaces before pushing.

- [ ] **Step 5: Commit**

```bash
git add README.md connector/codingagentconnector/README.md docs/design.md
git commit -m "docs: document cursor log synthesis"
```

---

## Self-Review Notes

- Spec coverage: claiming (Task 1), burst state/finalization/bounds/dedupe (Task 2), span construction incl. joins and allowlist (Task 3), fixtures/replay (Task 4), router + shared instruments (Task 5, including the gauge split and description neutralization the spec amendment records), validator helpers (Task 6), docs + design.md migration (Task 7). Privacy has no separate task because the allowlist is the mechanism and Tasks 3/6 assert it; the spec's privacy section adds no other behavior.
- Known deviations from codex patterns, all intentional: `Event` replaces codex's `agentEvent` naming to avoid stuttering `cursorEvent` in a package named cursor; burst dedupe keys on the wire's event id rather than a content fingerprint; the router carries real `Start`/`Shutdown` (the traces router's no-op embeds would break Codex drain).
