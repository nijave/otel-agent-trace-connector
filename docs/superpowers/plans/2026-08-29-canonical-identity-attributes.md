# Canonical Identity Attributes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carry a coding agent's user, team, machine, and terminal context into canonical output under `coding_agent.user.id`, `coding_agent.user.email`, `coding_agent.team.id`, `coding_agent.terminal.type`, and the standard OTel resource key `host.name`, controlled by a `capture_identity` config flag for the PII fields.

**Architecture:** A new `CaptureIdentity` bool on the connector config threads to every harness normalizer. Each normalizer writes the mapped identity keys onto its `invoke_agent` root span when the flag is on, and writes `coding_agent.terminal.type` unconditionally. `host.name` survives the shared resource filter only when the flag is on. Two closed allowlists (`canonicalAttributeKeys`, `canonicalResourceKeys`) gain the new keys so the keys pass conformance.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`/`pcommon`), the connector's `internal/canonical` contract package.

**Spec:** `docs/superpowers/specs/2026-08-29-canonical-identity-attributes-design.md`. Read it first; this plan builds its design and inherits its decisions.

## Global Constraints

- Prefix EVERY go command with `GOTOOLCHAIN=auto` (system go is older than the module floor; unprefixed go commands fail).
- `GOTOOLCHAIN=auto ./scripts/check.sh` must end `ALL CHECKS PASSED` before any push.
- Stage explicit paths only; never blanket `git add`.
- `capture_identity` defaults to `true`. When `false`, the four PII keys stay absent and the filter drops `host.name`, matching today's behavior for identity; `coding_agent.terminal.type` still appears.
- `coding_agent.user.id` and `coding_agent.team.id` are STRING attributes. Cursor's `cursor.user.id`/`cursor.team.id` arrive as int64 on the wire; cursor formats them to strings.
- Committed fixtures use SYNTHETIC identity only (e.g. `user@example.com`, a fake UUID, `host-01`). Never commit a real account email or id.
- Two goroutine-safe modules: run `go test` from the repo root AND from `connector/codingagentconnector` (the connector is its own module).
- The connector processes only logs and traces — no metrics. Do not add metrics handling.

## Mapping reference (the whole feature in one table)

| Canonical key | Raw source | Read from | Harness | Under flag |
| --- | --- | --- | --- | --- |
| `coding_agent.user.id` | `user.id` | span attr | claude | yes |
| `coding_agent.user.id` | `cursor.user.id` (int) | resource | cursor | yes |
| `coding_agent.user.id` | `ai.telemetry.metadata.userId` | span attr | opencode | yes |
| `coding_agent.user.id` | `lmnr.association.properties.user_id` | span attr | openhands | yes |
| `coding_agent.user.id` | `enduser.pseudo.id` | span attr | copilot (genai) | yes |
| `coding_agent.user.id` | `user.account_id` | log attr | codex | yes |
| `coding_agent.user.email` | `user.email` | log attr | codex | yes |
| `coding_agent.team.id` | `cursor.team.id` (int) | resource | cursor | yes |
| `host.name` (resource) | `host.name` | resource | codex | yes |
| `coding_agent.terminal.type` | `terminal.type` | log attr | codex | no |
| `coding_agent.terminal.type` | `terminal.type` | span attr | claude | no |

---

### Task 1: Config flag and plumbing (no identity written yet)

**Files:**
- Change: `connector/codingagentconnector/internal/codex/config.go`
- Change: `connector/codingagentconnector/factory.go:36-43`
- Change: `connector/codingagentconnector/traces.go:34-36`
- Change: `connector/codingagentconnector/internal/canonical/resource.go:44-48`
- Change: `connector/codingagentconnector/internal/claude/normalizer.go:30-32`, `internal/opencode/normalizer.go:39-41`, `internal/openhands/normalizer.go:85-87`, `internal/genai/normalizer.go:56-58`, `internal/pi/normalizer.go` (its `New`)
- Change: every `canonical.FilterResource(` call site (codex, cursor, claude, opencode, openhands, genai, pi)
- Test: `connector/codingagentconnector/config_test.go`

**Interfaces:**
- Produces: `codex.Config.CaptureIdentity bool` (mapstructure `capture_identity`, default true); `claude.New(next, captureIdentity)`, `opencode.New(next, captureIdentity)`, `openhands.New(next, captureIdentity)`, `genai.New(next, captureIdentity)`, `pi.New(next, captureIdentity)`; `canonical.FilterResource(rs ptrace.ResourceSpans, captureIdentity bool)`.

This task threads an unused flag and changes one signature. Behavior stays identical: `host.name` is still not in the allowlist yet, so `FilterResource` drops it regardless of the flag.

- [ ] **Step 1: Add the config field**

In `internal/codex/config.go`, add the field to the struct and default:

```go
// Config controls correlation state and turn finalization.
type Config struct {
	TurnTimeout     time.Duration `mapstructure:"turn_timeout"`
	ReorderWindow   time.Duration `mapstructure:"reorder_window"`
	MaxActiveTurns  int           `mapstructure:"max_active_turns"`
	MaxEvents       int           `mapstructure:"max_events_per_turn"`
	CaptureIdentity bool          `mapstructure:"capture_identity"`
}
```

In `NewDefaultConfig`, set `CaptureIdentity: true`:

```go
func NewDefaultConfig() *Config {
	return &Config{
		TurnTimeout:     defaultTurnTimeout,
		ReorderWindow:   defaultReorderWindow,
		MaxActiveTurns:  defaultMaxActive,
		MaxEvents:       defaultMaxEvents,
		CaptureIdentity: true,
	}
}
```

Leave `Validate` unchanged (a bool needs no rule).

- [ ] **Step 2: Change the resource filter signature (behavior unchanged)**

In `internal/canonical/resource.go`, change `FilterResource` to take the flag. Do NOT add `host.name` to the allowlist yet — that is Task 2.

```go
// FilterResource strips every resource attribute outside the canonical
// resource vocabulary from rs. When captureIdentity is false the identity
// resource keys (host.name) are stripped even if present. Edges call it after
// copying a raw input resource; reads of raw resource keys (such as
// session.id) must happen before the call.
func FilterResource(rs ptrace.ResourceSpans, captureIdentity bool) {
	rs.Resource().Attributes().RemoveIf(func(key string, _ pcommon.Value) bool {
		if !IsCanonicalResourceKey(key) {
			return true
		}
		if key == "host.name" && !captureIdentity {
			return true
		}
		return false
	})
}
```

- [ ] **Step 3: Thread the flag through the traces router and factory**

In `factory.go`, pass the config into `newTracesRouter`:

```go
func createTracesToTraces(
	_ context.Context,
	_ connector.Settings,
	cfg component.Config,
	next consumer.Traces,
) (connector.Traces, error) {
	return newTracesRouter(cfg.(*Config), next), nil
}
```

In `traces.go`, take the config and thread `CaptureIdentity` into each edge:

```go
func newTracesRouter(cfg *Config, next consumer.Traces) connector.Traces {
	id := cfg.CaptureIdentity
	return &tracesRouter{edges: []connector.Traces{
		claude.New(next, id), genai.New(next, id), opencode.New(next, id), pi.New(next, id), openhands.New(next, id),
	}}
}
```

- [ ] **Step 4: Add the captureIdentity field to each traces-path normalizer**

For each of `claude`, `opencode`, `openhands`, `genai`, `pi`: add a `captureIdentity bool` field to the normalizer struct and set it in `New`. Example for claude (`internal/claude/normalizer.go`):

```go
type claudeTraceNormalizer struct {
	next            consumer.Traces
	captureIdentity bool
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless Claude Code traces-to-traces edge.
func New(next consumer.Traces, captureIdentity bool) connector.Traces {
	return &claudeTraceNormalizer{next: next, captureIdentity: captureIdentity}
}
```

Do the same for `opencode`, `openhands`, `genai`, and `pi` (identical shape; each stores the bool). `pi` stores it only to pass to `FilterResource` — it writes no identity spans.

- [ ] **Step 5: Update every FilterResource call site to pass the flag**

Pass the harness's flag at each call:
- `internal/codex/trace.go:48` → `canonical.FilterResource(rs, captureIdentity)` (codex threads it from the edge struct; see Task 3 Step 1 for how the edge holds it — for THIS task, thread `turn`-side plumbing so `buildTrace` receives the bool and passes it; if that is not yet wired, pass `false` here and finish the wiring in Task 3). Prefer wiring the codex `buildTrace` bool now so no temporary value remains.
- `internal/cursor/trace.go:45` → `canonical.FilterResource(rs, captureIdentity)` (cursor threads from its edge struct; wire `buildTrace` to receive the bool).
- `internal/claude/normalizer.go:55` → `canonical.FilterResource(rs, n.captureIdentity)`
- `internal/opencode/normalizer.go:60` → `canonical.FilterResource(rs, n.captureIdentity)`
- `internal/openhands/normalizer.go:106` → `canonical.FilterResource(rs, n.captureIdentity)`
- `internal/genai/normalizer.go:96` → `canonical.FilterResource(rs, n.captureIdentity)`
- `internal/pi/normalizer.go` (its FilterResource call) → `canonical.FilterResource(rs, n.captureIdentity)`

For codex and cursor, the edge structs already hold `*Config` (they take it in `New(cfg, set, next)`); thread `cfg.CaptureIdentity` down to `buildTrace(turn, reason, scopeVersion, captureIdentity)` and pass it here. Update the `buildTrace` signature and its callers in the same task so the package builds.

- [ ] **Step 6: Add a config test for the default and decoding**

Append to `connector/codingagentconnector/config_test.go`:

```go
func TestDefaultConfigCapturesIdentity(t *testing.T) {
	cfg := createDefaultConfig()
	if !cfg.CaptureIdentity {
		t.Fatal("capture_identity must default to true")
	}
}
```

- [ ] **Step 7: Run the gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: `ALL CHECKS PASSED`. No fixture changes yet — behavior stays unchanged because the allowlist omits `host.name` and no normalizer writes identity.

- [ ] **Step 8: Commit**

```bash
git add connector/codingagentconnector/internal/codex/config.go connector/codingagentconnector/factory.go connector/codingagentconnector/traces.go connector/codingagentconnector/internal/canonical/resource.go connector/codingagentconnector/internal/claude/normalizer.go connector/codingagentconnector/internal/opencode/normalizer.go connector/codingagentconnector/internal/openhands/normalizer.go connector/codingagentconnector/internal/genai/normalizer.go connector/codingagentconnector/internal/pi/normalizer.go connector/codingagentconnector/internal/codex/trace.go connector/codingagentconnector/internal/cursor/trace.go connector/codingagentconnector/config_test.go
git commit -m "feat(connector): add capture_identity config flag and thread it to normalizers"
```

---

### Task 2: Vocabulary additions and host.name capture

**Files:**
- Change: `connector/codingagentconnector/internal/canonical/vocabulary.go:17-56`
- Change: `connector/codingagentconnector/internal/canonical/resource.go:14-20`
- Test: `connector/codingagentconnector/internal/canonical/conformance_test.go` (a `FilterResource` unit test)

**Interfaces:**
- Consumes: `FilterResource(rs, captureIdentity bool)` from Task 1.
- Produces: the four span keys and `host.name` are canonical, so any normalizer that writes them passes the allowlist.

- [ ] **Step 1: Write the failing resource-filter test**

Append to `internal/canonical/conformance_test.go`:

```go
func TestFilterResourceHostNameUnderIdentity(t *testing.T) {
	build := func() ptrace.ResourceSpans {
		traces := ptrace.NewTraces()
		rs := traces.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "codex")
		rs.Resource().Attributes().PutStr("host.name", "host-01")
		rs.Resource().Attributes().PutStr("vendor.thing", "x")
		return rs
	}
	on := build()
	FilterResource(on, true)
	if _, ok := on.Resource().Attributes().Get("host.name"); !ok {
		t.Fatal("host.name must survive when captureIdentity is true")
	}
	if _, ok := on.Resource().Attributes().Get("vendor.thing"); ok {
		t.Fatal("vendor.thing must be stripped")
	}
	off := build()
	FilterResource(off, false)
	if _, ok := off.Resource().Attributes().Get("host.name"); ok {
		t.Fatal("host.name must be stripped when captureIdentity is false")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/canonical/ -run TestFilterResourceHostNameUnderIdentity ; cd ../../..`
Expected: FAIL (host.name stripped even when true, because the allowlist does not yet include it).

- [ ] **Step 3: Add the keys to both allowlists**

In `internal/canonical/vocabulary.go`, add to `canonicalAttributeKeys` (after the existing `coding_agent.client.version` line):

```go
	"coding_agent.user.id",
	"coding_agent.user.email",
	"coding_agent.team.id",
	"coding_agent.terminal.type",
```

In `internal/canonical/resource.go`, add `host.name` to `canonicalResourceKeys`:

```go
var canonicalResourceKeys = []string{
	"service.name",
	"service.version",
	"telemetry.sdk.name",
	"telemetry.sdk.language",
	"telemetry.sdk.version",
	"host.name",
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/canonical/ -run TestFilterResourceHostNameUnderIdentity ; cd ../../..`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/canonical/vocabulary.go connector/codingagentconnector/internal/canonical/resource.go connector/codingagentconnector/internal/canonical/conformance_test.go
git commit -m "feat(canonical): admit identity span keys and host.name to the vocabulary"
```

---

### Task 3: Codex identity mapping and fixture

**Files:**
- Change: `connector/codingagentconnector/internal/codex/trace.go:97-115` (`putRootAttributes`)
- Verify: codex edge wiring so `buildTrace` receives `captureIdentity` (started in Task 1)
- Test: `connector/codingagentconnector/internal/codex/trace_test.go`
- Fixture: `connector/codingagentconnector/internal/codex/testdata/codex-native-logs.json` (extend one record with synthetic identity), and regenerate `codex-canonical.otlp.json` if the conformance test replays it

**Interfaces:**
- Consumes: `captureIdentity bool` threaded into `buildTrace` → `putRootAttributes`.
- Produces: codex roots carry `coding_agent.user.id` (from `user.account_id`), `coding_agent.user.email`, `coding_agent.terminal.type`, and resource `host.name` when the flag is on; `terminal.type` also when off.

- [ ] **Step 1: Write the failing test**

Add to `internal/codex/trace_test.go` (use the existing test helpers for building a turn; mirror an existing `buildTrace` test's setup). The test builds one turn whose events carry the identity metadata and asserts the root:

```go
func TestBuildTraceCapturesIdentity(t *testing.T) {
	turn := turnForTest() // existing helper that returns a *turnState with events
	// Ensure an event carries the codex common metadata:
	// user.account_id, user.email, terminal.type on turn.events[...] attrs,
	// and host.name on turn.resource. Set them with synthetic values:
	turn.resource["host.name"] = "host-01"
	setEventAttr(turn, "user.account_id", "acct-123")
	setEventAttr(turn, "user.email", "user@example.com")
	setEventAttr(turn, "terminal.type", "ghostty")

	traces, err := buildTrace(turn, "quiet", DefaultScopeVersion, true)
	require.NoError(t, err)
	root := spansByName(traces)["invoke_agent codex"][0]
	require.Equal(t, "acct-123", stringAttrOn(t, root, "coding_agent.user.id"))
	require.Equal(t, "user@example.com", stringAttrOn(t, root, "coding_agent.user.email"))
	require.Equal(t, "ghostty", stringAttrOn(t, root, "coding_agent.terminal.type"))
	require.Equal(t, "host-01", resourceStringAttr(t, traces, "host.name"))

	off, err := buildTrace(turn, "quiet", DefaultScopeVersion, false)
	require.NoError(t, err)
	rootOff := spansByName(off)["invoke_agent codex"][0]
	_, hasUser := rootOff.Attributes().Get("coding_agent.user.id")
	require.False(t, hasUser)
	// terminal.type is not under the flag:
	require.Equal(t, "ghostty", stringAttrOn(t, rootOff, "coding_agent.terminal.type"))
	_, hasHost := off.ResourceSpans().At(0).Resource().Attributes().Get("host.name")
	require.False(t, hasHost)
}
```

If `turnForTest`/`setEventAttr`/`resourceStringAttr` helpers do not exist, add small local helpers in the test file following the patterns already used in `trace_test.go` (build a `turnState`, append an `agentEvent` with an attrs map, read a resource attribute from the emitted traces).

- [ ] **Step 2: Run it to verify it fails**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/codex/ -run TestBuildTraceCapturesIdentity ; cd ../../..`
Expected: FAIL — the identity keys are absent (and, until Step 3 wiring, `buildTrace` may not accept the bool).

- [ ] **Step 3: Write the mapping**

In `internal/codex/trace.go`, change `putRootAttributes` to take the flag and write the keys, and pass the flag from `buildTrace`:

```go
func putRootAttributes(attrs pcommon.Map, turn *turnState, events []agentEvent, captureIdentity bool) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "codex")
	attrs.PutStr("gen_ai.provider.name", "openai")
	attrs.PutStr("gen_ai.conversation.id", turn.conversationID)
	attrs.PutStr("coding_agent.client.name", "codex")
	sourceEvent := "codex.user_prompt"
	if !turn.promptSeen && len(events) > 0 {
		sourceEvent = events[0].name
	}
	attrs.PutStr("coding_agent.source.event", sourceEvent)
	attrs.PutStr("coding_agent.source", "normalized")
	if model := lastStringAttr(events, "model"); model != "" {
		attrs.PutStr("gen_ai.request.model", model)
	}
	if version := lastStringAttr(events, "app.version"); version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
	// terminal.type carries no identity, so the flag does not control it.
	if terminal := lastStringAttr(events, "terminal.type"); terminal != "" {
		attrs.PutStr("coding_agent.terminal.type", terminal)
	}
	if captureIdentity {
		if id := lastStringAttr(events, "user.account_id"); id != "" {
			attrs.PutStr("coding_agent.user.id", id)
		}
		if email := lastStringAttr(events, "user.email"); email != "" {
			attrs.PutStr("coding_agent.user.email", email)
		}
	}
}
```

In `buildTrace`, accept `captureIdentity bool` and pass it to both `putRootAttributes(root.Attributes(), turn, events, captureIdentity)` and `canonical.FilterResource(rs, captureIdentity)`. Update all `buildTrace` callers (the finalizer path; the codex edge holds `*Config`, so pass `cfg.CaptureIdentity`).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/codex/ ; cd ../../..`
Expected: PASS (the whole codex package, so the conformance test against the fixture runs too).

- [ ] **Step 5: Extend the codex fixture with synthetic identity**

The committed `codex-native-logs.json` predates ChatGPT-auth identity. Add `user.account_id`, `user.email`, and `terminal.type` (synthetic values) to the log-record attributes of the fixture's records, and add `host.name` to its resource attributes. If `TestCodexConformance` or `TestConnectorAgainstRealCodexCapture` replays this fixture and compares against `codex-canonical.otlp.json`, regenerate that canonical fixture from the updated raw input (run the connector test that writes it, or hand-edit to add the four keys to the root/resource). Keep every value synthetic.

- [ ] **Step 6: Run the codex package and the gate**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/codex/ ; cd ../../.. && GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: codex tests PASS; gate ends `ALL CHECKS PASSED`.

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/codex/trace.go connector/codingagentconnector/internal/codex/trace_test.go connector/codingagentconnector/internal/codex/testdata/codex-native-logs.json connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json
git commit -m "feat(codex): map account id, email, terminal type, and host.name to canonical output"
```

---

### Task 4: Cursor identity mapping and fixture

**Files:**
- Change: `connector/codingagentconnector/internal/cursor/trace.go:71-86` (`putRootAttributes`)
- Test: `connector/codingagentconnector/internal/cursor/trace_test.go`
- Fixture: regenerate `connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json` from the committed raw fixture

**Interfaces:**
- Consumes: `captureIdentity bool` threaded into `buildTrace` → `putRootAttributes` (wired in Task 1).
- Produces: cursor roots carry `coding_agent.user.id` and `coding_agent.team.id` (string-formatted from the int resource values) when the flag is on.

- [ ] **Step 1: Write the failing test**

The cursor test helpers already build a burst with `testResourceRaw()` carrying `cursor.user.id: int64(99)` and `cursor.team.id: int64(4242)`. Add to `trace_test.go`:

```go
func TestBuildTraceCapturesIdentity(t *testing.T) {
	traces, err := buildTrace(burstForTest(), "quiet", "0.1.0", true)
	require.NoError(t, err)
	root := spansByName(traces)["invoke_agent cursor"][0]
	require.Equal(t, "99", stringAttrOn(t, root, "coding_agent.user.id"))
	require.Equal(t, "4242", stringAttrOn(t, root, "coding_agent.team.id"))

	off, err := buildTrace(burstForTest(), "quiet", "0.1.0", false)
	require.NoError(t, err)
	rootOff := spansByName(off)["invoke_agent cursor"][0]
	_, hasUser := rootOff.Attributes().Get("coding_agent.user.id")
	require.False(t, hasUser)
	_, hasTeam := rootOff.Attributes().Get("coding_agent.team.id")
	require.False(t, hasTeam)
}
```

(If the existing `buildTrace` signature does not yet take the bool, Task 1 added it; match that signature.)

- [ ] **Step 2: Run it to verify it fails**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/cursor/ -run TestBuildTraceCapturesIdentity ; cd ../../..`
Expected: FAIL — identity keys absent.

- [ ] **Step 3: Write the mapping**

In `internal/cursor/trace.go`, change `putRootAttributes` to take the flag and add a helper that formats an int-or-string resource value as a string:

```go
func putRootAttributes(attrs pcommon.Map, burst *burstState, events []Event, captureIdentity bool) {
	attrs.PutStr("gen_ai.operation.name", "invoke_agent")
	attrs.PutStr("gen_ai.agent.name", "cursor")
	attrs.PutStr("gen_ai.conversation.id", burst.conversationID)
	attrs.PutStr("coding_agent.client.name", "cursor")
	if len(events) > 0 {
		attrs.PutStr("coding_agent.source.event", events[0].Body)
	}
	attrs.PutStr("coding_agent.source", "normalized")
	copyStringAttr(attrs, burst.resource, "service.version", "coding_agent.client.version")
	if captureIdentity {
		if v := identityString(burst.resource, "cursor.user.id"); v != "" {
			attrs.PutStr("coding_agent.user.id", v)
		}
		if v := identityString(burst.resource, "cursor.team.id"); v != "" {
			attrs.PutStr("coding_agent.team.id", v)
		}
	}
}

// identityString reads a raw resource value as a string whether the wire sent
// it as a string or an int (cursor sends user/team ids as int64).
func identityString(src map[string]any, key string) string {
	if s := StringValue(src[key]); s != "" {
		return s
	}
	if n, ok := Int64Value(src[key]); ok {
		return strconv.FormatInt(n, 10)
	}
	return ""
}
```

Add `"strconv"` to the cursor package imports. Pass `captureIdentity` from `buildTrace` into `putRootAttributes` (Task 1 added the `buildTrace` bool).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/cursor/ ; cd ../../..`
Expected: PASS. Note: `trace_test.go:100-107` currently asserts `coding_agent.cursor.user.id` (the OLD renamed key) is absent — that assertion stays valid, since the new key is `coding_agent.user.id`, a different string. Do not remove it.

- [ ] **Step 5: Regenerate the cursor canonical fixture**

The cursor raw fixture (`cursor-native-logs.json`) carries `cursor.user.id`/`cursor.team.id`. With the flag defaulting on, the canonical fixture (`cursor-canonical.otlp.json`) now gains `coding_agent.user.id`/`coding_agent.team.id` on the root. Regenerate it deterministically by replaying the raw fixture through the connector (the cursor test that produces canonical output), and commit the updated bytes. Values come from the raw fixture (already synthetic: `99`/`4242`).

- [ ] **Step 6: Run the gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: `ALL CHECKS PASSED`.

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/cursor/trace.go connector/codingagentconnector/internal/cursor/trace_test.go connector/codingagentconnector/internal/cursor/testdata/cursor-canonical.otlp.json
git commit -m "feat(cursor): map user id and team id to canonical output"
```

---

### Task 5: Traces-path harness identity (claude, opencode, openhands, genai)

**Files:**
- Change: `internal/claude/normalizer.go:100-146` (`normalizeClaudeSpan`), pass `n.captureIdentity` in
- Change: `internal/opencode/normalizer.go:108-161` (`normalizeSpan`, `wireStreamText` case)
- Change: `internal/openhands/normalizer.go:252-352` (`emitGroup` → `putRootAttributes`)
- Change: `internal/genai/normalizer.go:139-174` (`normalizeSpan`)
- Test: each harness's `normalizer_test.go`
- Fixtures: regenerate the canonical fixtures for harnesses whose raw fixture carries an identity source (openhands, genai/copilot)

**Interfaces:**
- Consumes: the `captureIdentity` field on each normalizer struct (Task 1).
- Produces: `coding_agent.user.id` on each harness's `invoke_agent` root when the flag is on; claude also writes `coding_agent.terminal.type` always.

This is four same-shape edits. Do them together; each writes `coding_agent.user.id` on the root from a different raw key.

- [ ] **Step 1: Write the failing tests**

For each harness, add an on/off pair. Claude example (`internal/claude/normalizer_test.go`) — set `user.id` and `terminal.type` on the interaction span, run the normalizer with identity on and off, assert the root:

```go
func TestClaudeCapturesIdentity(t *testing.T) {
	// build input traces with a claude_code.interaction span carrying
	// user.id="u-1" and terminal.type="iterm" (reuse this file's builders)
	on := runNormalizer(t, buildClaudeInput(t), true)   // helper: New(sink, true), ConsumeTraces, return output
	root := findSpan(t, on, "invoke_agent claude_code")
	require.Equal(t, "u-1", attrString(t, root, "coding_agent.user.id"))
	require.Equal(t, "iterm", attrString(t, root, "coding_agent.terminal.type"))

	off := runNormalizer(t, buildClaudeInput(t), false)
	rootOff := findSpan(t, off, "invoke_agent claude_code")
	_, hasUser := rootOff.Attributes().Get("coding_agent.user.id")
	require.False(t, hasUser)
	require.Equal(t, "iterm", attrString(t, rootOff, "coding_agent.terminal.type")) // not under flag
}
```

Opencode: source `ai.telemetry.metadata.userId` on the `ai.streamText` span; assert `coding_agent.user.id` on `invoke_agent opencode`. Openhands: source `lmnr.association.properties.user_id`; assert on `invoke_agent openhands`. Genai: source `enduser.pseudo.id` on the invoke_agent span; assert on the root. Each pair asserts present-when-on, absent-when-off. Follow each file's existing helper conventions (the openhands and genai test files already build inputs and call `New`).

- [ ] **Step 2: Run them to verify they fail**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/claude/ ./internal/opencode/ ./internal/openhands/ ./internal/genai/ ; cd ../../..`
Expected: FAIL — identity keys absent.

- [ ] **Step 3: Write the claude mapping**

In `normalizeClaudeSpan`, change the signature to take the flag and, in the `claude_code.interaction` case, add the writes after the conversation id:

```go
func normalizeClaudeSpan(span ptrace.Span, version, resourceSessionID string, captureIdentity bool) {
	switch span.Name() {
	case "claude_code.interaction":
		span.SetName("invoke_agent claude_code")
		span.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
		span.Attributes().PutStr("gen_ai.agent.name", "claude_code")
		span.Attributes().PutStr("coding_agent.source.event", "claude_code.interaction")
		putClaudeCommon(span.Attributes(), version)
		sessionID := firstSpanString(span, "session.id", "session_id")
		if sessionID == "" {
			sessionID = resourceSessionID
		}
		if sessionID != "" {
			span.Attributes().PutStr("gen_ai.conversation.id", sessionID)
		}
		if terminal := firstSpanString(span, "terminal.type"); terminal != "" {
			span.Attributes().PutStr("coding_agent.terminal.type", terminal)
		}
		if captureIdentity {
			if uid := firstSpanString(span, "user.id"); uid != "" {
				span.Attributes().PutStr("coding_agent.user.id", uid)
			}
		}
	// ... other cases unchanged ...
	}
}
```

Update the call in `ConsumeTraces` (line 65) to `normalizeClaudeSpan(span, version, resourceSessionID, n.captureIdentity)`.

- [ ] **Step 4: Write the opencode mapping**

In `normalizeSpan`, take the flag and, in the `wireStreamText` case (the root), add:

```go
	case wireStreamText:
		span.SetParentSpanID(pcommon.SpanID{})
		sessionID := firstString(wire.Attributes(), "session.id")
		if sessionID == "" {
			sessionID = resourceSessionID
		}
		if sessionID != "" {
			attrs.PutStr("gen_ai.conversation.id", sessionID)
		}
		if captureIdentity {
			if uid := firstString(wire.Attributes(), "ai.telemetry.metadata.userId"); uid != "" {
				attrs.PutStr("coding_agent.user.id", uid)
			}
		}
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		// ... rest unchanged ...
```

Change `normalizeSpan`'s signature to accept `captureIdentity bool` and update its call in `ConsumeTraces` (line 73) to pass `n.captureIdentity`.

- [ ] **Step 5: Write the openhands mapping**

Add a constant near the other attr consts (`internal/openhands/normalizer.go:29-33`):

```go
	attrUserID = "lmnr.association.properties.user_id"
```

Thread the flag from `ConsumeTraces` (`emitGroup(ss.Spans(), groups[key], n.captureIdentity)`) → `emitGroup(dst, g, captureIdentity)` → `putRootAttributes(root.Attributes(), g, captureIdentity)`, and in `putRootAttributes` add after the conversation id:

```go
	if captureIdentity {
		if uid := firstString(src, attrUserID); uid != "" {
			attrs.PutStr("coding_agent.user.id", uid)
		}
	}
```

- [ ] **Step 6: Write the genai mapping**

In `normalizeSpan`, take the flag and write user id only on the invoke_agent root, reading the raw span attr before `content.Strip` runs:

```go
func normalizeSpan(span ptrace.Span, scopeName, serviceName, serviceVersion string, captureIdentity bool) {
	attrs := span.Attributes()
	operationValue, ok := attrs.Get("gen_ai.operation.name")
	if !ok {
		return
	}
	operation := operationValue.Str()
	// ... existing name/provider/usage logic unchanged ...
	if captureIdentity && operation == "invoke_agent" {
		if v, ok := attrs.Get("enduser.pseudo.id"); ok && v.Str() != "" {
			attrs.PutStr("coding_agent.user.id", v.Str())
		}
	}
	attrs.PutStr("coding_agent.source", "native")
	// ... rest unchanged ...
}
```

Update the call in `ConsumeTraces` (line 102) to pass `n.captureIdentity`.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd connector/codingagentconnector && GOTOOLCHAIN=auto go test ./internal/claude/ ./internal/opencode/ ./internal/openhands/ ./internal/genai/ ; cd ../../..`
Expected: PASS.

- [ ] **Step 8: Regenerate the affected canonical fixtures**

The genai copilot fixture carries `enduser.pseudo.id` (`user-42`) and the openhands raw fixture carries `lmnr.association.properties.user_id` (`42`). With the flag defaulting on, their canonical fixtures gain `coding_agent.user.id`. Regenerate each deterministically from its committed raw fixture (the harness test that produces canonical output) and commit the updated bytes. The claude and opencode canonical fixtures gain identity only if their raw captures carried the source key — if `claude-native-traces.json` carries `user.id`/`terminal.type` (it carries `terminal.type`; check for `user.id`), regenerate `claude-canonical.otlp.json` too. Confirm every value is synthetic.

- [ ] **Step 9: Run the gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: `ALL CHECKS PASSED`.

- [ ] **Step 10: Commit**

```bash
git add connector/codingagentconnector/internal/claude/ connector/codingagentconnector/internal/opencode/ connector/codingagentconnector/internal/openhands/ connector/codingagentconnector/internal/genai/
git commit -m "feat(connector): map user id to canonical output for claude, opencode, openhands, and genai"
```

---

### Task 6: Validator assertions and documentation

**Files:**
- Change: `e2e/validator/validator.go` (positive identity assertions) and `e2e/validator/validator_test.go` (fixture cases already exist)
- Change: `docs/harnesses/claude-code.md`, `docs/harnesses/codex.md`, `docs/harnesses/cursor.md`, `docs/harnesses/opencode.md`, `docs/harnesses/openhands.md`, `docs/harnesses/genai-scopes.md`
- Change: `docs/design.md`, and the config reference (search `docs/` and `README.md` for the `turn_timeout`/`reorder_window` config table)

**Interfaces:**
- Consumes: the canonical keys from Tasks 2-5.
- Produces: nothing later tasks depend on (terminal task).

- [ ] **Step 1: Add a validator identity check**

In `e2e/validator/validator.go`, after `collectRunSpans`/`rejectSensitiveAttrs` in `validateCanonicalTraces`, the new keys already pass because they are canonical. Add a helper the fixture tests call to confirm an identity key appears on the invoke_agent root, and use it in the existing per-fixture tests that carry identity. Example helper:

```go
// rootHasAttr reports whether the invoke_agent root for agent carries key.
func rootHasAttr(traces ptrace.Traces, runID, key string) bool {
	for _, span := range collectRunSpans(traces, runID) {
		if op, ok := span.Attributes().Get("gen_ai.operation.name"); ok && op.Str() == "invoke_agent" {
			if _, ok := span.Attributes().Get(key); ok {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 2: Assert identity in the fixture tests that carry it**

In `e2e/validator/validator_test.go`, extend the codex and copilot fixture tests (added in the canonical-fixtures work) to assert `coding_agent.user.id` appears via `fileRoutingHomes`-style file loading — reuse `validateTraceFile` to load the fixture, then `require.True(t, rootHasAttr(traces, runID, "coding_agent.user.id"))`. Add codex `coding_agent.user.email` and `coding_agent.terminal.type` assertions, and confirm `host.name` survives on the codex resource. Keep the existing positive `validateCanonicalFile` calls unchanged.

- [ ] **Step 3: Run the validator tests and the gate**

Run: `cd e2e/validator && GOTOOLCHAIN=auto go test ./... ; cd ../.. && GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: validator tests PASS; gate ends `ALL CHECKS PASSED`.

- [ ] **Step 4: Update the harness docs**

In each `docs/harnesses/*.md`, change the identity rows from "dropped" to the new mapping. Concretely: claude `user.id` → `coding_agent.user.id`, `terminal.type` → `coding_agent.terminal.type`; codex add rows for `user.account_id` → `coding_agent.user.id`, `user.email` → `coding_agent.user.email`, `terminal.type` → `coding_agent.terminal.type`, `host.name` → kept as resource `host.name`; cursor `cursor.user.id` → `coding_agent.user.id`, `cursor.team.id` → `coding_agent.team.id`; opencode `ai.telemetry.metadata.userId` → `coding_agent.user.id`; openhands `lmnr.association.properties.user_id` → `coding_agent.user.id`; genai `enduser.pseudo.id` → `coding_agent.user.id`. Note each identity mapping happens only when `capture_identity` is on. Keep the prose lint clean — the repo's Vale hook enforces the personal banned-word list and the verb rules, so fix any line it flags.

- [ ] **Step 5: Document the config setting**

In `docs/design.md` and the config reference table (wherever the config reference lists `turn_timeout`/`max_active_turns`), add `capture_identity` (default `true`) with a note that it controls the PII identity keys (`coding_agent.user.id`, `coding_agent.user.email`, `coding_agent.team.id`, and resource `host.name`), that it defaults on so an upgrade begins emitting identity, and that `coding_agent.terminal.type` ships unconditionally. Same prose-word constraints as Step 4.

- [ ] **Step 6: Run the gate**

Run: `GOTOOLCHAIN=auto ./scripts/check.sh`
Expected: `ALL CHECKS PASSED`.

- [ ] **Step 7: Commit**

```bash
git add e2e/validator/validator.go e2e/validator/validator_test.go docs/harnesses/ docs/design.md README.md
git commit -m "docs: record canonical identity attributes and the capture_identity setting"
```

---

## Plan self-review notes

- **Spec coverage:** config flag (Task 1); vocabulary + host.name (Task 2); codex user.id/email/terminal/host.name (Task 3); cursor user.id/team.id (Task 4); claude/opencode/openhands/genai user.id + claude terminal.type (Task 5); validator + docs (Task 6). `coding_agent.session.id` stays out of scope (spec decision); no metrics handling (connector has none).
- **Flag mechanism:** the four PII keys write only under `n.captureIdentity`/`cfg.CaptureIdentity`; `host.name` survives `FilterResource` only when the flag is on; `coding_agent.terminal.type` writes unconditionally. This matches the spec exactly.
- **Type consistency:** `coding_agent.user.id`/`coding_agent.team.id` are strings everywhere; cursor formats its int64 ids via `identityString`. All seven call sites use `FilterResource(rs, captureIdentity bool)` identically.
- **Fixture safety:** every regenerated or extended fixture uses synthetic identity; the plan never commits a real account email or id.
- **Deviation from the spec's sketch:** the spec described `FilterResource` taking a bool; this plan keeps that and additionally lists `host.name` in `canonicalResourceKeys` so conformance accepts it when present — the retention itself stays controlled by the flag inside `FilterResource`.
