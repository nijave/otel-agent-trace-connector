# Signal Switch and Cost Key Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `signals` config list that turns the connector's logs or traces consumption off, and a canonical `coding_agent.usage.cost` key mapped from the three wires that report a per-request cost (Pi, OpenHands, Copilot).

**Architecture:** The switch is one slice field on the shared `codex.Config` plus an early return in each router; the edges stay constructed so Start/Shutdown and the conformance edge count stay the same. The cost key joins the closed vocabulary and the doc mirror, then three stateless normalizers each copy their wire's cost onto chat spans, absent when the wire has none.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`/`pcommon`), confmap, testify, the connector's `internal/canonical` contract package.

**Spec:** `docs/superpowers/specs/2026-10-19-harness-feature-gaps-design.md`, sections 1 and 2. Read it first.

## Global Constraints

- Work from `connector/codingagentconnector/` for Go commands (a separate module; no `go.work`). Prefix every `go` command with `GOTOOLCHAIN=go1.27.0` (the system Go is 1.26).
- `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh` from the repo root must end `ALL CHECKS PASSED` before pushing.
- `coding_agent.usage.cost` is a **double**, USD, **chat spans only**, copied verbatim, **absent when the wire carries no cost**. Never 0-fill it.
- `signals` defaults to `["logs", "traces"]`; an empty list, an unknown value, or a duplicate fails `Validate`.
- Stage explicit paths only; never blanket `git add`.
- Every committed fixture value stays synthetic (`user@example.com`, `host-01`, fake ids).
- Matrix docs record conditional coverage as `mapped (absent when …)`, derived from the code's presence guards.
- Do not run the live e2e scripts (`scripts/e2e*.sh`) until Task 9; they cost money.

## Review Focus

1. **`signals: [traces]` on a logs pipeline must not drop records for the pipeline's other exporters.** The connector never mutates input (`MutatesData: false`), so a no-op `ConsumeLogs` leaves records flowing. Task 2's test asserts the sink gets nothing and the input keeps every record.
2. **A cost of `0` on the wire must map as `0`, not disappear.** Copilot's BYOK capture reports `0`. Task 7 tests a zero-valued cost.
3. **A string-typed cost must not crash or map garbage.** Pi's blob can deliver numbers as strings on some paths. Task 5 tests a string `"0.0001"` (maps) and `"abc"` (absent).
4. **OpenHands cost must land exactly once after the wrapper fold.** The wrapper carries it, the inner span does not; after folding, the chat span carries it once, and a wrapper with no inner child keeps it. Task 6 tests both.
5. **`signals` decoded from YAML must replace the default, not append to it.** confmap replaces slices, but Task 1 pins it with a decode test (`signals: [logs]` yields exactly `[logs]`).

---

### Task 1: `signals` config field and validation

**Files:**
- Change: `connector/codingagentconnector/internal/codex/config.go`
- Test: `connector/codingagentconnector/config_test.go`

**Interfaces:**
- Produces: `codex.SignalLogs = "logs"`, `codex.SignalTraces = "traces"`, `Config.Signals []string` (mapstructure `signals`), `func (c *Config) ConsumesLogs() bool`, `func (c *Config) ConsumesTraces() bool`.

- [ ] **Step 1: Write the failing tests**

Append to `connector/codingagentconnector/config_test.go`:

```go
func TestDefaultConfigConsumesBothSignals(t *testing.T) {
	cfg := createDefaultConfig()
	require.Equal(t, []string{"logs", "traces"}, cfg.Signals)
	require.True(t, cfg.ConsumesLogs())
	require.True(t, cfg.ConsumesTraces())
}

func TestConfigValidateSignals(t *testing.T) {
	tests := []struct {
		name      string
		signals   []string
		wantError string
	}{
		{name: "logs only", signals: []string{"logs"}},
		{name: "traces only", signals: []string{"traces"}},
		{name: "empty", signals: []string{}, wantError: "signals"},
		{name: "unknown", signals: []string{"metrics"}, wantError: `"metrics"`},
		{name: "duplicate", signals: []string{"logs", "logs"}, wantError: "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig()
			cfg.Signals = tt.signals
			err := cfg.Validate()
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

// TestConfigDecodeSignalsReplacesDefault pins that a configured list replaces
// the default rather than merging with it: `signals: [logs]` must turn traces
// off.
func TestConfigDecodeSignalsReplacesDefault(t *testing.T) {
	cfg := createDefaultConfig()
	conf := confmap.NewFromStringMap(map[string]any{"signals": []any{"logs"}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, []string{"logs"}, cfg.Signals)
	require.True(t, cfg.ConsumesLogs())
	require.False(t, cfg.ConsumesTraces())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test . -run 'TestDefaultConfigConsumesBothSignals|TestConfigValidateSignals|TestConfigDecodeSignalsReplacesDefault' -v`
Expected: FAIL to compile with `cfg.Signals undefined`.

- [ ] **Step 3: Write the field**

Replace the contents of `connector/codingagentconnector/internal/codex/config.go` with:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"errors"
	"fmt"
	"time"
)

const (
	defaultTurnTimeout   = 10 * time.Minute
	defaultReorderWindow = 30 * time.Second
	defaultMaxActive     = 10_000
	defaultMaxEvents     = 1_000

	// SignalLogs and SignalTraces are the values the signals list accepts.
	SignalLogs   = "logs"
	SignalTraces = "traces"
)

// Config controls correlation state, turn finalization, and which input
// signals the connector consumes.
type Config struct {
	TurnTimeout     time.Duration `mapstructure:"turn_timeout"`
	ReorderWindow   time.Duration `mapstructure:"reorder_window"`
	MaxActiveTurns  int           `mapstructure:"max_active_turns"`
	MaxEvents       int           `mapstructure:"max_events_per_turn"`
	CaptureIdentity bool          `mapstructure:"capture_identity"`
	// Signals lists the input signals the connector consumes: "logs",
	// "traces", or both. A signal left out passes through the connector
	// untouched, so its records still reach the pipeline's other exporters.
	Signals []string `mapstructure:"signals"`
}

// NewDefaultConfig returns an independent default configuration.
func NewDefaultConfig() *Config {
	return &Config{
		TurnTimeout:     defaultTurnTimeout,
		ReorderWindow:   defaultReorderWindow,
		MaxActiveTurns:  defaultMaxActive,
		MaxEvents:       defaultMaxEvents,
		CaptureIdentity: true,
		Signals:         []string{SignalLogs, SignalTraces},
	}
}

// Validate rejects values which would make state unbounded or finalization ambiguous.
func (c *Config) Validate() error {
	if c.TurnTimeout <= 0 {
		return errors.New("turn_timeout must be positive")
	}
	if c.ReorderWindow < 0 {
		return errors.New("reorder_window must not be negative")
	}
	if c.ReorderWindow >= c.TurnTimeout {
		return errors.New("reorder_window must be less than turn_timeout")
	}
	if c.MaxActiveTurns <= 0 {
		return errors.New("max_active_turns must be positive")
	}
	if c.MaxEvents <= 0 {
		return errors.New("max_events_per_turn must be positive")
	}
	if len(c.Signals) == 0 {
		return errors.New("signals must list at least one of logs, traces")
	}
	seen := map[string]bool{}
	for _, signal := range c.Signals {
		if signal != SignalLogs && signal != SignalTraces {
			return fmt.Errorf("signals: unknown signal %q (want logs or traces)", signal)
		}
		if seen[signal] {
			return fmt.Errorf("signals: %q listed twice", signal)
		}
		seen[signal] = true
	}
	return nil
}

// ConsumesLogs reports whether the logs-to-traces edge is on.
func (c *Config) ConsumesLogs() bool { return c.consumes(SignalLogs) }

// ConsumesTraces reports whether the traces-to-traces edge is on.
func (c *Config) ConsumesTraces() bool { return c.consumes(SignalTraces) }

func (c *Config) consumes(signal string) bool {
	for _, s := range c.Signals {
		if s == signal {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOTOOLCHAIN=go1.27.0 go test . -run 'TestConfig|TestDefaultConfig|TestFactory' -v`
Expected: PASS for every test, including the pre-existing `TestConfigValidate` and `TestFactoryDefaultsAreIndependent`.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/codex/config.go connector/codingagentconnector/config_test.go
git commit -m "feat(config): add signals list selecting logs and/or traces"
```

### Task 2: Routers honor the switch

**Files:**
- Change: `connector/codingagentconnector/logs.go`
- Change: `connector/codingagentconnector/traces.go`
- Test: `connector/codingagentconnector/logs_test.go`
- Test: `connector/codingagentconnector/traces_test.go`

**Interfaces:**
- Consumes: `Config.ConsumesLogs()`, `Config.ConsumesTraces()` from Task 1.
- Produces: `logsRouter.consume bool`, `tracesRouter.consume bool`; no exported change.

- [ ] **Step 1: Write the failing tests**

Append to `connector/codingagentconnector/logs_test.go`:

```go
// TestLogsRouterSkipsRecordsWhenLogsSignalOff pins the signals switch on the
// logs edge: with logs off, nothing reaches the canonical sink and the input
// batch is left intact for the pipeline's other exporters.
func TestLogsRouterSkipsRecordsWhenLogsSignalOff(t *testing.T) {
	sink := &logsSink{}
	cfg := createDefaultConfig()
	cfg.Signals = []string{"traces"}
	set := connector.Settings{TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	logsEdge, err := newLogsRouter(cfg, set, sink)
	require.NoError(t, err)
	require.NoError(t, logsEdge.Start(context.Background(), nil))

	input := mixedLogs(t)
	before := input.LogRecordCount()
	require.NoError(t, logsEdge.ConsumeLogs(context.Background(), input))
	require.NoError(t, logsEdge.Shutdown(context.Background()))
	require.Empty(t, sink.traces, "logs off: the edges must emit nothing")
	require.Equal(t, before, input.LogRecordCount(), "input must pass through untouched")
}
```

Append to `connector/codingagentconnector/traces_test.go`:

```go
// TestTracesRouterSkipsSpansWhenTracesSignalOff pins the signals switch on
// the traces edge: with traces off, a Claude group produces no canonical
// output.
func TestTracesRouterSkipsSpansWhenTracesSignalOff(t *testing.T) {
	input := ptrace.NewTraces()
	claudeGroup := input.ResourceSpans().AppendEmpty()
	claudeGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("claude_code.interaction")

	cfg := createDefaultConfig()
	cfg.Signals = []string{"logs"}
	sink := &routerSink{}
	router := newTracesRouter(cfg, sink)
	require.NoError(t, router.ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.traces, "traces off: the edges must emit nothing")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test . -run 'SignalOff' -v`
Expected: both FAIL on `require.Empty` (the routers still emit).

- [ ] **Step 3: Gate the routers**

In `connector/codingagentconnector/logs.go`, change the struct and constructor and `ConsumeLogs`:

```go
// logsRouter fans the logs-to-traces edge across the provider claimers. Each
// edge ignores foreign records (Codex claims codex.-prefixed event names,
// Cursor claims the cursor.telemetry scope), so a record lands in at most one
// edge and unclaimed records stay out of the canonical edge. Unlike the
// stateless tracesRouter, Start and Shutdown fan out: both edges own sweep
// loops and drain-on-shutdown state. consume mirrors the signals config: when
// logs are off the router is a pass-through and the edges never see a record.
type logsRouter struct {
	edges   []connector.Logs
	consume bool
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
	return &logsRouter{edges: []connector.Logs{codexEdge, cursorEdge}, consume: cfg.ConsumesLogs()}, nil
}
```

and

```go
func (r *logsRouter) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	if !r.consume {
		return nil
	}
	for _, edge := range r.edges {
		if err := edge.ConsumeLogs(ctx, logs); err != nil {
			return err
		}
	}
	return nil
}
```

In `connector/codingagentconnector/traces.go`:

```go
type tracesRouter struct {
	edges   []connector.Traces
	consume bool
	component.StartFunc
	component.ShutdownFunc
}

func newTracesRouter(cfg *Config, next consumer.Traces) connector.Traces {
	id := cfg.CaptureIdentity
	return &tracesRouter{edges: []connector.Traces{
		claude.New(next, id), genai.New(next, id), opencode.New(next, id), pi.New(next, id), openhands.New(next, id),
	}, consume: cfg.ConsumesTraces()}
}
```

and

```go
func (r *tracesRouter) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	if !r.consume {
		return nil
	}
	for _, edge := range r.edges {
		if err := edge.ConsumeTraces(ctx, traces); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test . -count=1`
Expected: PASS (the conformance registry still counts seven wired edges because the edges are still constructed).

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/logs.go connector/codingagentconnector/traces.go connector/codingagentconnector/logs_test.go connector/codingagentconnector/traces_test.go
git commit -m "feat(router): skip consumption for signals turned off in config"
```

### Task 3: Document the switch

**Files:**
- Change: `README.md` (the "Collector configuration" section, around lines 208-256)

- [ ] **Step 1: Add the key to the example and a paragraph**

In the YAML example, add `signals: [logs, traces]` after `capture_identity: true`. After the `capture_identity` paragraph (ending "so it ships regardless of this setting."), add:

```markdown
`signals` lists which input signals a `coding_agent` instance consumes and
defaults to both. Set `signals: [traces]` on an instance that sits in a logs
pipeline to make it a pass-through: the records still reach every other
exporter in that pipeline, and the connector emits nothing from them. This
matters for a harness that sends both signals (Codex with `otel.trace_exporter`
set, for example), where consuming both would produce two canonical traces per
turn. Pipeline wiring is the other way to choose: a connector instance only
sees the signals of the pipelines that list it as an exporter, as the example
above does with `coding_agent` and `coding_agent/claude`.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: describe the signals switch"
```

### Task 4: Vocabulary gains `coding_agent.usage.cost`

**Files:**
- Change: `connector/codingagentconnector/internal/canonical/vocabulary.go:50`
- Change: `docs/canonical-attributes.md` (generated block and the connector-namespace paragraphs)
- Test: `connector/codingagentconnector/internal/canonical/conformance_test.go` (existing `TestVocabularyDocsMirror`)

- [ ] **Step 1: Add the key and watch the mirror test fail**

In `vocabulary.go`, insert `"coding_agent.usage.cost",` on the line after `"coding_agent.request.service_tier",`.

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/canonical/ -run TestVocabularyDocsMirror -v`
Expected: FAIL, printing the block the doc must contain.

- [ ] **Step 2: Update the doc**

In `docs/canonical-attributes.md`, inside the `<!-- vocabulary:generated -->` block, insert `- `coding_agent.usage.cost`` on the line after `- `coding_agent.request.service_tier``. After the `coding_agent.request.service_tier` paragraph (the one ending "one key keeps cross-provider queries uniform."), add:

```markdown
`coding_agent.usage.cost` records the per-request cost in US dollars as a double, copied verbatim from the wire onto chat spans when the harness reports one (Pi's serialized `usage.cost.total`, OpenHands' `gen_ai.usage.cost`, Copilot's `github.copilot.cost`) and absent otherwise. Every source is the harness's own estimate from its pricing table, not an invoice, so the key serves trend and comparison queries, not billing. It sits in the connector namespace because the GenAI registry defines no cost attribute.
```

In the "Connector namespace" provenance bullet, add `coding_agent.usage.cost` after `coding_agent.request.service_tier`.

- [ ] **Step 3: Run the mirror test**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/canonical/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add connector/codingagentconnector/internal/canonical/vocabulary.go docs/canonical-attributes.md
git commit -m "feat(canonical): add coding_agent.usage.cost to the vocabulary"
```

### Task 5: Pi maps `usage.cost.total`

**Files:**
- Change: `connector/codingagentconnector/internal/pi/normalizer.go` (`mapUsage` and `blobCount` area, lines 254-302)
- Test: `connector/codingagentconnector/internal/pi/normalizer_test.go`
- Change: `connector/codingagentconnector/internal/pi/conformance_test.go` (0.1.28 case signals)
- Change: `docs/harnesses/pi.md:95`

**Interfaces:**
- Produces: `mapCost(attrs, wire pcommon.Map, blob map[string]any)` (package-private).

- [ ] **Step 1: Write the failing test**

Append to `connector/codingagentconnector/internal/pi/normalizer_test.go`:

```go
// TestPiTraceNormalizerMapsCost pins coding_agent.usage.cost: the 0.1.28
// serialized usage object nests cost.total; the 0.1.9 wire carried a flat
// usage.cost.total. A missing or malformed cost maps nothing.
func TestPiTraceNormalizerMapsCost(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		flat  map[string]string
		want  float64
		found bool
	}{
		{name: "blob cost.total", usage: `{"input":4,"cost":{"input":0.0000004,"total":0.000181775}}`, want: 0.000181775, found: true},
		{name: "blob string total", usage: `{"input":4,"cost":{"total":"0.0001"}}`, want: 0.0001, found: true},
		{name: "blob zero total", usage: `{"input":4,"cost":{"total":0}}`, want: 0, found: true},
		{name: "flat fallback", usage: `{"input":4}`, flat: map[string]string{"usage.cost.total": "0.00002297"}, want: 0.00002297, found: true},
		{name: "blob wins over flat", usage: `{"cost":{"total":0.5}}`, flat: map[string]string{"usage.cost.total": "0.25"}, want: 0.5, found: true},
		{name: "malformed total", usage: `{"cost":{"total":"abc"}}`, found: false},
		{name: "no cost anywhere", usage: `{"input":4}`, found: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ptrace.NewTraces()
			rs := input.ResourceSpans().AppendEmpty()
			scope := rs.ScopeSpans().AppendEmpty()
			scope.Scope().SetName("@amaster.ai/pi-telemetry")
			llm := scope.Spans().AppendEmpty()
			llm.SetName("llm-generation [main] [request]")
			llm.Attributes().PutStr("requestedModel", "glm-5.3-flash")
			llm.Attributes().PutStr("usage", tc.usage)
			for k, v := range tc.flat {
				llm.Attributes().PutStr(k, v)
			}
			sink := &traceSink{}
			require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
			chat := findSpan(t, sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans(), "chat glm-5.3-flash")
			value, ok := chat.Attributes().Get("coding_agent.usage.cost")
			require.Equal(t, tc.found, ok)
			if tc.found {
				require.Equal(t, pcommon.ValueTypeDouble, value.Type())
				require.InDelta(t, tc.want, value.Double(), 1e-12)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/pi/ -run TestPiTraceNormalizerMapsCost -v`
Expected: FAIL on the `found` cases (`coding_agent.usage.cost` absent).

- [ ] **Step 3: Write the code**

In `normalizer.go`, change the end of `mapUsage` and add helpers. Replace the `mapUsage` function with:

```go
// mapUsage writes the canonical token counts, preferring the serialized usage
// object per field and falling back to the flat source key when the object
// omits it. A zero count is mapped: the wire reports no cache as 0 rather than
// by omission. The per-request cost rides the same object (0.1.28 nests it as
// cost.total) or the older flat usage.cost.total.
func mapUsage(attrs, wire pcommon.Map) {
	blob := usageBlob(wire)
	for _, counter := range usageCounters {
		if count, ok := blobCount(blob, counter.field); ok {
			attrs.PutInt(counter.canonicalKey, count)
			continue
		}
		if counter.flatKey == "" {
			continue
		}
		if value, ok := wire.Get(counter.flatKey); ok {
			if count, ok := coerceInt(value); ok {
				attrs.PutInt(counter.canonicalKey, count)
			}
		}
	}
	mapCost(attrs, wire, blob)
}

// mapCost copies the harness's per-request cost estimate onto
// coding_agent.usage.cost (USD, double). The serialized object's cost.total
// wins; the 0.1.9 wire's flat usage.cost.total is the fallback. Nothing maps
// when neither parses as a number.
func mapCost(attrs, wire pcommon.Map, blob map[string]any) {
	if cost, ok := blob["cost"].(map[string]any); ok {
		if total, ok := blobFloat(cost, "total"); ok {
			attrs.PutDouble("coding_agent.usage.cost", total)
			return
		}
	}
	if value, ok := wire.Get("usage.cost.total"); ok {
		if total, ok := coerceFloat(value); ok {
			attrs.PutDouble("coding_agent.usage.cost", total)
		}
	}
}

// blobFloat coerces one member to a float: OTLP JSON numbers decode as
// float64 and some paths deliver numbers as strings.
func blobFloat(blob map[string]any, field string) (float64, bool) {
	switch value := blob[field].(type) {
	case float64:
		return value, true
	case string:
		total, err := strconv.ParseFloat(value, 64)
		return total, err == nil
	default:
		return 0, false
	}
}

func coerceFloat(value pcommon.Value) (float64, bool) {
	switch value.Type() {
	case pcommon.ValueTypeDouble:
		return value.Double(), true
	case pcommon.ValueTypeInt:
		return float64(value.Int()), true
	case pcommon.ValueTypeStr:
		total, err := strconv.ParseFloat(value.Str(), 64)
		return total, err == nil
	default:
		return 0, false
	}
}
```

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/pi/ -count=1`
Expected: PASS. If `TestPiTraceNormalizerRebuildsCanonicalTree` asserts `usage.cost.total` is absent from output, that still holds (the raw key never survives; only the canonical key is new).

- [ ] **Step 5: Add the conformance signal**

In `connector/codingagentconnector/internal/pi/conformance_test.go`, in the 0.1.28 case's `signals` slice (the one using `RawField`), append:

```go
{RawKey: "usage", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/pi/ -run Conformance -count=1`
Expected: PASS (the committed 0.1.28 fixture's usage object carries `cost.total`).

- [ ] **Step 6: Update the matrix**

In `docs/harnesses/pi.md`, replace the row

```
| `usage` → `cost`, `usage` → `cacheWrite1h`, `usage.cost.total` | — | dropped (no canonical counterpart; cost detail stays in the raw pipeline) |
```

with

```
| `usage` → `cost` → `total` | `coding_agent.usage.cost` | mapped (USD double, 0.1.28 serialized object; absent when the object has no numeric `cost.total`; the extension prices from its own model table, so the value is an estimate) |
| `usage.cost.total` | `coding_agent.usage.cost` | mapped (0.1.9 flat key, fallback only when the serialized object carries no `cost.total`) |
| `usage` → `cost` → `input`/`output`/`cacheRead`/`cacheWrite`, `usage` → `cacheWrite1h` | — | dropped (per-component cost breakdown and the 1h cache-write counter have no canonical counterpart) |
```

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/pi/normalizer.go connector/codingagentconnector/internal/pi/normalizer_test.go connector/codingagentconnector/internal/pi/conformance_test.go docs/harnesses/pi.md
git commit -m "feat(pi): map usage cost total to coding_agent.usage.cost"
```

### Task 6: OpenHands carries `gen_ai.usage.cost` through the wrapper fold

**Files:**
- Change: `connector/codingagentconnector/internal/openhands/normalizer.go` (`foldUsage` line 360, `normalizeChat` line 429)
- Test: `connector/codingagentconnector/internal/openhands/normalizer_test.go`
- Change: `connector/codingagentconnector/internal/openhands/conformance_test.go`
- Regenerate: `connector/codingagentconnector/internal/openhands/testdata/openhands-canonical.otlp.json`
- Change: `docs/harnesses/openhands.md:110`

- [ ] **Step 1: Write the failing tests**

Append to `connector/codingagentconnector/internal/openhands/normalizer_test.go` (it already has `makeSpan`, `makeTraces`, `normalizeOne`, `findByName`, `baseAttrs`, `traceA`):

```go
// TestWrapperCostFoldsOntoChatSpan pins coding_agent.usage.cost: the SDK writes
// gen_ai.usage.cost on the llm.<model> wrapper only, so after the fold the
// inner chat span carries it exactly once; an inner span that already reports a
// cost keeps its own.
func TestWrapperCostFoldsOntoChatSpan(t *testing.T) {
	wrapperAttrs := baseAttrs()
	wrapperAttrs["gen_ai.usage.cost"] = 0.00073285
	innerAttrs := baseAttrs()
	innerAttrs["gen_ai.request.model"] = "glm-5.3-flash"
	out := normalizeOne(t, makeTraces(
		makeSpan(traceA, spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}),
		makeSpan(traceA, spanSpec{name: "llm.glm-5.3-flash", spanID: "bbbbbbbbbbbbbbbb", parentID: "aaaaaaaaaaaaaaaa", spanType: "LLM", attrs: wrapperAttrs}),
		makeSpan(traceA, spanSpec{name: "litellm.completion", spanID: "cccccccccccccccc", parentID: "bbbbbbbbbbbbbbbb", spanType: "LLM", attrs: innerAttrs}),
	))
	chats := findByName(t, out, "chat glm-5.3-flash")
	require.Len(t, chats, 1)
	cost, ok := chats[0].Attributes().Get("coding_agent.usage.cost")
	require.True(t, ok, "the wrapper's cost must land on the folded chat span")
	require.InDelta(t, 0.00073285, cost.Double(), 1e-12)
	_, raw := chats[0].Attributes().Get("gen_ai.usage.cost")
	require.False(t, raw, "the wire key must not survive")

	innerAttrs["gen_ai.usage.cost"] = 0.5
	out = normalizeOne(t, makeTraces(
		makeSpan(traceA, spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}),
		makeSpan(traceA, spanSpec{name: "llm.glm-5.3-flash", spanID: "bbbbbbbbbbbbbbbb", parentID: "aaaaaaaaaaaaaaaa", spanType: "LLM", attrs: wrapperAttrs}),
		makeSpan(traceA, spanSpec{name: "litellm.completion", spanID: "cccccccccccccccc", parentID: "bbbbbbbbbbbbbbbb", spanType: "LLM", attrs: innerAttrs}),
	))
	cost, _ = findByName(t, out, "chat glm-5.3-flash")[0].Attributes().Get("coding_agent.usage.cost")
	require.InDelta(t, 0.5, cost.Double(), 1e-12, "the inner span's own cost wins over the wrapper's")
}

// TestWrapperWithoutInnerKeepsCost pins that a wrapper with no inner chat child
// stays a chat span and keeps its cost.
func TestWrapperWithoutInnerKeepsCost(t *testing.T) {
	wrapperAttrs := baseAttrs()
	wrapperAttrs["gen_ai.usage.cost"] = 0.00017819
	out := normalizeOne(t, makeTraces(
		makeSpan(traceA, spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}),
		makeSpan(traceA, spanSpec{name: "llm.glm-5.3-flash", spanID: "bbbbbbbbbbbbbbbb", parentID: "aaaaaaaaaaaaaaaa", spanType: "LLM", attrs: wrapperAttrs}),
	))
	chats := findByName(t, out, "chat")
	require.Len(t, chats, 1)
	cost, ok := chats[0].Attributes().Get("coding_agent.usage.cost")
	require.True(t, ok)
	require.InDelta(t, 0.00017819, cost.Double(), 1e-12)
}

// TestChatWithoutCostCarriesNoCostKey pins the absence rule.
func TestChatWithoutCostCarriesNoCostKey(t *testing.T) {
	innerAttrs := baseAttrs()
	innerAttrs["gen_ai.request.model"] = "glm-5.3-flash"
	out := normalizeOne(t, makeTraces(
		makeSpan(traceA, spanSpec{name: "conversation", spanID: "aaaaaaaaaaaaaaaa", attrs: baseAttrs()}),
		makeSpan(traceA, spanSpec{name: "litellm.completion", spanID: "cccccccccccccccc", parentID: "aaaaaaaaaaaaaaaa", spanType: "LLM", attrs: innerAttrs}),
	))
	_, ok := findByName(t, out, "chat glm-5.3-flash")[0].Attributes().Get("coding_agent.usage.cost")
	require.False(t, ok)
}
```

`putRaw` in the test helpers handles `string`, `int64`, `bool`, and `[]string` only. Add a `float64` case to it:

```go
	case float64:
		attrs.PutDouble(key, value)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -run 'Cost' -v`
Expected: the two "carries cost" tests FAIL on `require.True(t, ok)`.

- [ ] **Step 3: Write the code**

In `normalizer.go`, add a constant near `attrIsDelegate`:

```go
	wireCost      = "gen_ai.usage.cost"
	canonicalCost = "coding_agent.usage.cost"
```

In `normalizeChat`, after the `usageKeys` loop, add:

```go
	if cost, ok := wireAttrs.Get(wireCost); ok && cost.Type() == pcommon.ValueTypeDouble {
		attrs.PutDouble(canonicalCost, cost.Double())
	}
```

In `foldUsage`, after the `usageKeys` loop, add:

```go
	if _, exists := attrs.Get(canonicalCost); !exists {
		if cost, ok := wrapper.Attributes().Get(wireCost); ok && cost.Type() == pcommon.ValueTypeDouble {
			attrs.PutDouble(canonicalCost, cost.Double())
		}
	}
```

Update `foldUsage`'s doc comment to end: "the provider did not report to lmnr (the cache counters) and the SDK's per-request cost estimate."

- [ ] **Step 4: Run the package tests and see the fixture test fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -count=1`
Expected: the new tests PASS; `TestFixtureReplayMatchesCanonicalFixture` FAILS because the canonical fixture lacks the new key (the committed raw fixture carries `gen_ai.usage.cost` on both wrapper spans).

- [ ] **Step 5: Regenerate the canonical fixture**

Create a throwaway file `connector/codingagentconnector/internal/openhands/regen_test.go`:

```go
package openhands

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
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "openhands-canonical.otlp.json"), out, 0o644))
}
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -run TestRegenerateCanonicalFixture -count=1`
Then delete the file: `rm connector/codingagentconnector/internal/openhands/regen_test.go`.

Check the diff: `git diff --stat connector/codingagentconnector/internal/openhands/testdata/` should show only the canonical file changed, and `rg -c coding_agent.usage.cost` on it should report 2 (one per chat span). Keep the file formatted the way the previous commit left it (if the previous file was pretty-printed, run `jq . ` over it and compare `git diff` noise; the replay test compares with `JSONEq`, so formatting does not affect tests).

- [ ] **Step 6: Add the conformance signal**

In `connector/codingagentconnector/internal/openhands/conformance_test.go`, append to the signals slice:

```go
{RawKey: "gen_ai.usage.cost", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
```

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -count=1`
Expected: PASS.

- [ ] **Step 7: Update the matrix**

In `docs/harnesses/openhands.md`, replace the `gen_ai.usage.cost` row (line 110) with:

```
| `gen_ai.usage.cost` | `coding_agent.usage.cost` | mapped (USD double; the SDK writes it on the `llm.<model>` wrapper, and the fold carries it onto the inner chat span when that span reports none of its own; absent when the SDK has no price for the model) |
```

- [ ] **Step 8: Commit**

```bash
git add connector/codingagentconnector/internal/openhands/normalizer.go connector/codingagentconnector/internal/openhands/normalizer_test.go connector/codingagentconnector/internal/openhands/conformance_test.go connector/codingagentconnector/internal/openhands/testdata/openhands-canonical.otlp.json docs/harnesses/openhands.md
git commit -m "feat(openhands): carry the SDK cost estimate onto the chat span"
```

### Task 7: Copilot maps `github.copilot.cost` on chat spans

**Files:**
- Change: `connector/codingagentconnector/internal/genai/normalizer.go` (`normalizeSpan`, after `mapTTFT(attrs)`)
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`
- Change: `connector/codingagentconnector/internal/genai/conformance_test.go` (copilot-cli signals)
- Change: `connector/codingagentconnector/conformance_test.go` (copilot-cli signals in the registry)
- Change: `docs/harnesses/genai-scopes.md:208`

- [ ] **Step 1: Write the failing test**

Append to `connector/codingagentconnector/internal/genai/normalizer_test.go` (it has `newGroup` and `traceSink`):

```go
// TestGenAINormalizerMapsCopilotCostOnChat pins coding_agent.usage.cost for
// Copilot: the vendor cost maps on chat spans (zero included), never on
// invoke_agent, and the raw key never survives.
func TestGenAINormalizerMapsCopilotCostOnChat(t *testing.T) {
	input := ptrace.NewTraces()
	chat := newGroup(input, "github.copilot", "chat")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutDouble("github.copilot.cost", 0)
	root := newGroup(input, "github.copilot", "invoke_agent")
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutDouble("github.copilot.cost", 0.15)
	bare := newGroup(input, "github.copilot", "chat")
	bare.Attributes().PutStr("gen_ai.operation.name", "chat")

	sink := &traceSink{}
	require.NoError(t, New(sink, true).ConsumeTraces(context.Background(), input))
	// The edge keeps resource-group order, so output spans line up with the
	// three newGroup calls above.
	var out []ptrace.Span
	for _, traces := range sink.all() {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			out = append(out, traces.ResourceSpans().At(i).ScopeSpans().At(0).Spans().At(0))
		}
	}
	require.Len(t, out, 3)
	chatSpan, rootSpan, bareSpan := out[0], out[1], out[2]
	require.Equal(t, "invoke_agent", rootSpan.Name())
	cost, ok := chatSpan.Attributes().Get("coding_agent.usage.cost")
	require.True(t, ok, "a zero cost is still a cost")
	require.Equal(t, pcommon.ValueTypeDouble, cost.Type())
	require.Equal(t, 0.0, cost.Double())
	_, ok = chatSpan.Attributes().Get("github.copilot.cost")
	require.False(t, ok)
	_, ok = rootSpan.Attributes().Get("coding_agent.usage.cost")
	require.False(t, ok, "cost is a chat-span key; the root's vendor copy drops")
	_, ok = bareSpan.Attributes().Get("coding_agent.usage.cost")
	require.False(t, ok, "no wire cost, no key")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/genai/ -run TestGenAINormalizerMapsCopilotCostOnChat -v`
Expected: FAIL on `require.True(t, ok, "a zero cost is still a cost")`.

- [ ] **Step 3: Write the code**

In `normalizeSpan`, after `mapTTFT(attrs)`, add:

```go
	mapCopilotCost(attrs, operation)
```

and add the function:

```go
// mapCopilotCost copies GitHub Copilot's per-request cost estimate onto
// coding_agent.usage.cost on chat spans. The invoke_agent copy is a session
// rollup and drops with the rest of the vendor namespace.
func mapCopilotCost(attrs pcommon.Map, operation string) {
	if operation != "chat" {
		return
	}
	if cost, ok := attrs.Get("github.copilot.cost"); ok && cost.Type() == pcommon.ValueTypeDouble {
		attrs.PutDouble("coding_agent.usage.cost", cost.Double())
	}
}
```

The content strip that runs after normalization removes `github.copilot.cost`.

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/genai/ -count=1`
Expected: PASS.

- [ ] **Step 5: Add the conformance signals**

In `connector/codingagentconnector/internal/genai/conformance_test.go`, in the `copilot-cli` edge's signals, append:

```go
{RawKey: "github.copilot.cost", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
```

In `connector/codingagentconnector/conformance_test.go`, add the same line to the `copilot-cli` fixture's signals, and add the Pi and OpenHands presence signals from Tasks 5 and 6 to their registry entries:

```go
{RawKey: "usage", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
```

(pi) and

```go
{RawKey: "gen_ai.usage.cost", CanonicalKey: "coding_agent.usage.cost", Kind: canonical.Presence},
```

(openhands).

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Update the matrix**

In `docs/harnesses/genai-scopes.md`, replace line 208 (`| `github.copilot.cost` | chat, invoke_agent | — | dropped |`) with:

```
| `github.copilot.cost` | chat | `coding_agent.usage.cost` | mapped (USD double, Copilot's own estimate; the pinned 1.0.100 BYOK capture reports `0`; absent when the span carries no numeric cost) |
| `github.copilot.cost` | invoke_agent | — | dropped (session rollup of the chat costs) |
```

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/genai/normalizer.go connector/codingagentconnector/internal/genai/normalizer_test.go connector/codingagentconnector/internal/genai/conformance_test.go connector/codingagentconnector/conformance_test.go docs/harnesses/genai-scopes.md
git commit -m "feat(genai): map Copilot cost onto chat spans"
```

### Task 8: "Not provided" rows for the other harnesses

**Files:**
- Change: `docs/harnesses/claude-code.md` ("Canonical keys with no Claude Code source" table)
- Change: `docs/harnesses/codex.md` ("Canonical keys with no Codex source" table)
- Change: `docs/harnesses/opencode.md` ("Canonical keys with no OpenCode source" table)
- Change: `docs/harnesses/cursor.md` ("Canonical keys with no Cursor source" table)

- [ ] **Step 1: Add one row to each table**

Claude Code: `| `coding_agent.usage.cost` | not provided (no per-request cost on the traces wire; `claude_code.cost.usage` is a metric) |`

Codex: `| `coding_agent.usage.cost` | not provided (`codex.turn_cost` is a log-only event the edge does not read, and the pinned capture carries none) |`

OpenCode: `| `coding_agent.usage.cost` | not provided (the AI SDK spans carry no cost) |`

Cursor: `| `coding_agent.usage.cost` | not provided (`cursor.cost.usage` is a metric with no conversation id; the logs carry no cost) |`

Also in `docs/harnesses/genai-scopes.md`'s "Canonical usage keys with no GenAI-edge source" table add: `| `coding_agent.usage.cost` | openai_v2, util-genai, strands | not provided (Copilot provides it) |`

- [ ] **Step 2: Commit**

```bash
git add docs/harnesses/claude-code.md docs/harnesses/codex.md docs/harnesses/opencode.md docs/harnesses/cursor.md docs/harnesses/genai-scopes.md
git commit -m "docs: record which harnesses carry no per-request cost"
```

### Task 9: Full check and live stacks

**Files:** none new.

- [ ] **Step 1: Run the repo check**

Run from the repo root: `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`. Fix anything it reports (gofmt, lint, mdatagen freshness) and amend the owning commit.

- [ ] **Step 2: Run the three affected live stacks**

The Pi, OpenHands, and Copilot edges changed. Export the keys from the keyring per `e2e/README.md` (never echo them), then:

```bash
GOTOOLCHAIN=go1.27.0 ./scripts/e2e-pi.sh
GOTOOLCHAIN=go1.27.0 ./scripts/e2e-openhands.sh
GOTOOLCHAIN=go1.27.0 ./scripts/e2e-copilot.sh
```

Expected: each ends with the validator test passing. After each, confirm the new key appears:

```bash
rg -c coding_agent.usage.cost .e2e-output/canonical-traces.json
```

Expected: at least 1 for each stack (Copilot reports `0` but the key is present).

- [ ] **Step 3: Open the PR**

Push the branch and open a PR titled `feat: signals switch and canonical cost key` with the spec path and the three live-stack results in the body.
