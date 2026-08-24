# Canonical Attribute Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every supported harness emit canonical spans that align 1:1 with the OTel GenAI semantic conventions — explicit key-by-key remapping, no pass-through — and enforce it with a cross-harness conformance test.

**Architecture:** A new `internal/canonical` package owns the vocabulary as data and a `Conformance` runner; each of the seven edges registers its native testdata fixture plus source-backed signals (`sum` for token counters, `presence` for TTFT/finish reasons). `internal/content` delegates its runtime allowlist to the same vocabulary. Each edge then gets an atomic fix commit: remap usage/TTFT/finish reasons, drop everything outside the vocabulary. Docs: one shared canonical list + one matrix document per harness.

**Tech Stack:** Go (collector pdata: `ptrace`, `plog`, `pcommon`), standard `testing`.

**Spec:** `docs/superpowers/specs/2026-08-23-canonical-attribute-contract-design.md` — read it first; this plan argues from it.

## Global Constraints

- Canonical attribute set (complete, from spec): `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.request.max_tokens`, `gen_ai.request.stream`, `gen_ai.response.finish_reasons`, `gen_ai.response.id`, `gen_ai.response.model`, `gen_ai.response.time_to_first_chunk`, `gen_ai.server.time_to_first_token`, `gen_ai.agent.id`, `gen_ai.agent.name`, `gen_ai.agent.version`, `gen_ai.conversation.id`, `gen_ai.tool.call.id`, `gen_ai.tool.name`, `gen_ai.tool.type`, `gen_ai.tool.status`, `gen_ai.event.start_time`, `gen_ai.event.end_time`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.usage.total_tokens`, `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_creation.input_tokens`, `gen_ai.usage.reasoning.output_tokens`, `server.address`, `server.port`, `exception.type`, `exception.message`, `exception.escaped`, `exception.stacktrace`, `coding_agent.source`, `coding_agent.source.scope`, `coding_agent.source.event`, `coding_agent.client.name`, `coding_agent.client.version`.
- No `gen_ai.usage.` wildcard anywhere. Unknown usage keys are vendor keys.
- Required on every emitted span: `gen_ai.operation.name`, `coding_agent.source`, `coding_agent.client.name`.
- The repo has NO go.work; build/test each module in its own directory.
- Run `./scripts/check.sh` before pushing anything. Never push red.
- Commits are atomic per task, staged with explicit paths.
- mdatagen outputs are untouched by this work (span attributes are not component telemetry).

---

### Task 1: `internal/canonical` contract package

**Files:**
- Create: `connector/codingagentconnector/internal/canonical/vocabulary.go`
- Create: `connector/codingagentconnector/internal/canonical/conformance.go`
- Test: `connector/codingagentconnector/internal/canonical/conformance_test.go`

**Interfaces:**
- Produces: `IsCanonicalAttribute(key string) bool`, `RequiredKeys() []string`, `SignalKind` (`Sum`, `Presence`), `Signal{RawKey, CanonicalKey string; Kind SignalKind}`, `RawInput{Traces ptrace.Traces; Logs plog.Logs}`, `Edge{Name string; LoadRaw func() (RawInput, error); Normalize func(RawInput) (ptrace.Traces, error); Signals []Signal}`, `Check(e Edge) []string`, `Conformance(t *testing.T, e Edge)`. Later tasks consume all of these; do not rename them.

- [ ] **Step 1: Write the failing tests**

```go
package canonical

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func spanWithAttrs(kv map[string]any) ptrace.Span {
	rs := ptrace.NewResourceSpans()
	ss := rs.ScopeSpans().AppendEmpty()
	s := ss.Spans().AppendEmpty()
	for k, v := range kv {
		switch val := v.(type) {
		case int64:
			s.Attributes().PutInt(k, val)
		case string:
			s.Attributes().PutStr(k, val)
		}
	}
	return s
}

func TestCheckRequired(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		_ = spanWithAttrs(map[string]any{"gen_ai.usage.input_tokens": int64(5)}) // missing required
		return out, nil
	}}
	errs := Check(e)
	if len(errs) == 0 || !contains(errs, "required") {
		t.Fatalf("want required-key failure, got %v", errs)
	}
}

func TestCheckAllowed(t *testing.T) {
	e := Edge{Name: "t", Normalize: func(RawInput) (ptrace.Traces, error) {
		out := ptrace.NewTraces()
		_ = spanWithAttrs(map[string]any{
			"gen_ai.operation.name":     "chat",
			"coding_agent.source":       "native",
			"coding_agent.client.name":  "x",
			"github.copilot.cost":       1.5,
			"gen_ai.usage.totalTokens2": int64(1), // unknown usage key must fail
		})
		return out, nil
	}}
	errs := Check(e)
	if len(errs) != 2 || !contains(errs, "github.copilot.cost") || !contains(errs, "totalTokens2") {
		t.Fatalf("want exactly the two vendor-key failures, got %v", errs)
	}
}

func TestCheckSumSignal(t *testing.T) {
	raw := func() (RawInput, error) {
		in := RawInput{Traces: ptrace.NewTraces()}
		_ = spanWithAttrs(map[string]any{"input_tokens": int64(10)})
		in.Traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().Attributes().PutInt("input_tokens", 10)
		return in, nil
	}
	// raw carries 10; output carries only 4 → mismatch
	e := Edge{Name: "t", LoadRaw: raw, Signals: []Signal{{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: Sum}},
		Normalize: func(in RawInput) (ptrace.Traces, error) {
			out := ptrace.NewTraces()
			s := out.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
			s.Attributes().PutStr("gen_ai.operation.name", "chat")
			s.Attributes().PutStr("coding_agent.source", "native")
			s.Attributes().PutStr("coding_agent.client.name", "x")
			s.Attributes().PutInt("gen_ai.usage.input_tokens", 4)
			return out, nil
		}}
	if errs := Check(e); len(errs) == 0 || !contains(errs, "gen_ai.usage.input_tokens") {
		t.Fatalf("want sum mismatch, got %v", errs)
	}
}
```

Add a `contains([]string, string) bool` helper and a presence-signal test (raw present, output absent → failure; raw absent → signal skipped). Also unit-test `IsCanonicalAttribute`: every key in Global Constraints line 1 returns true; `gen_ai.usage.reasoning_tokens` (unknown family member), `ai.usage.inputTokens`, `event_loop.cycle_id` return false.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/canonical/ -v`
Expected: FAIL (package does not compile / functions undefined)

- [ ] **Step 3: Implement**

`vocabulary.go` holds the constraint list as two slices (`canonicalAttributeKeys`, `canonicalAttributePrefixes` containing ONLY `"exception."`) plus `requiredKeys`; `IsCanonicalAttribute` mirrors content.go's current logic shape. `conformance.go` implements `Check`:

1. `out := e.Normalize(raw)` after `raw := e.LoadRaw()`.
2. Required: walk every span in `out`; missing key ⇒ error `"harness %s: span %q missing required %s"`.
3. Allowed: walk spans AND surviving events' attributes; any key failing `IsCanonicalAttribute` ⇒ error naming harness and key.
4. Signals: compute `rawTotal` by walking `raw.Traces` span attributes and `raw.Logs` record attributes with an int/double/string numeric coercion helper (copy semantics from cursor/event.go `Int64Value`). If `rawTotal == 0 && !rawPresent`, skip the signal. `Sum`: compare against total of `CanonicalKey` across output spans; mismatch names both totals. `Presence`: require ≥1 occurrence.
5. `Conformance(t, e)` runs `Check` and calls `t.Errorf` per entry prefixed with the harness name.

- [ ] **Step 4: Run tests until green**

Run: `cd connector/codingagentconnector && go test ./internal/canonical/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/canonical
git commit -m "canonical: add attribute-contract vocabulary and conformance runner"
```

---

### Task 2: Rename `telemetry.source` → `coding_agent.source`

**Files:**
- Modify: all seven normalizers under `connector/codingagentconnector/internal/{claude,codex,cursor,genai,opencode,openhands,pi}/` (the `PutStr("telemetry.source", ...)` sites)
- Modify: `connector/codingagentconnector/internal/content/content.go` (allowlist entry)
- Modify: every test/fixture referencing `telemetry.source` (find via grep)

- [ ] **Step 1: Find every reference**

Run: `rg -l 'telemetry\.source' connector docs README.md`
Note each file.

- [ ] **Step 2: Replace mechanically**

Replace the literal `telemetry.source` with `coding_agent.source` in code, tests, and JSON fixtures. Do not touch `coding_agent.source.scope`/`.event` (already correct).

- [ ] **Step 3: Verify**

Run: `rg 'telemetry\.source' connector docs` — expect zero hits.
Run: `cd connector/codingagentconnector && go test ./...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -u connector docs
git commit -m "canonical: rename telemetry.source to coding_agent.source"
```

---

### Task 3: Single-source vocabulary; shrink `content.go`; refresh stale fixtures

**Files:**
- Modify: `connector/codingagentconnector/internal/content/content.go` — delete `canonicalAttributeKeys`/`canonicalAttributePrefixes` bodies, delegate to `canonical.IsCanonicalAttribute` (import `internal/canonical`)
- Modify: `connector/codingagentconnector/internal/genai/testdata/{strands-copilot,strands-canonical,openai-adhoc-*}.otlp.json`, `copilot-cli-canonical.otlp.json` — remove keys the shrunk allowlist now rejects

**Interfaces:**
- Consumes: `canonical.IsCanonicalAttribute` (Task 1).
- Produces: runtime strip enforces exactly the Task-1 vocabulary; `content.Strip` signature unchanged.

- [ ] **Step 1: Failing test** — add to `content` or `canonical` tests: assert `IsCanonicalAttribute("github.copilot.cost") == false`, `("event_loop.cycle_id") == false`, `("gen_ai.usage.cache_read.input_tokens") == true`, `("gen_ai.usage.reasoning.output_tokens") == true`. Run; expect fail while content.go still lists copilot/event_loop keys.

- [ ] **Step 2: Shrink vocabulary** — in `canonical/vocabulary.go` remove `github.copilot.*` (all 13), `event_loop.*`, and keep everything else from Global Constraints. Rewrite content.go's maps to delegate:

```go
func isCanonicalAttribute(key string) bool { return canonical.IsCanonicalAttribute(key) }
```

Delete content.go's local tables entirely (single source of truth lives in canonical).

- [ ] **Step 3: Fix fixtures/tests broken by the shrink**

Run: `cd connector/codingagentconnector && go test ./internal/... `
For each fixture-replay failure, edit the captured canonical fixture to remove the now-forbidden keys (`gen_ai.tool.description`, `gen_ai.tool.json_schema`, `github.copilot.git.branch`, `github.copilot.context.*`, `github.copilot.agent.type`). Do NOT change raw fixtures. Where tests asserted presence of dropped keys, invert to assert absence.

- [ ] **Step 4: Full module green**

Run: `cd connector/codingagentconnector && go test ./... && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal
git commit -m "canonical: single-source vocabulary, drop vendor namespaces from runtime allowlist"
```

---

### Task 4: Claude edge — explicit remap, end passthrough-plus

**Files:**
- Modify: `connector/codingagentconnector/internal/claude/normalizer.go`
- Modify: `connector/codingagentconnector/internal/claude/normalizer_test.go`
- Create: `connector/codingagentconnector/internal/claude/conformance_test.go`
- Create: `docs/harnesses/claude-code.md`

**Mappings (llm_request → chat):** `input_tokens`→`gen_ai.usage.input_tokens`; `output_tokens`→`gen_ai.usage.output_tokens`; `cache_read_tokens`→`gen_ai.usage.cache_read.input_tokens`; `cache_creation_tokens`→`gen_ai.usage.cache_creation.input_tokens`; `ttft_ms`→`gen_ai.response.time_to_first_chunk`; `stop_reason`→ append value into `gen_ai.response.finish_reasons` (string slice) when not already present; existing `model` fallback stays. **Dropped:** `duration_ms`, `speed`, `llm_request.context`, `attempt`, `success`, `session.id` (after conversation mapping), `span.type`, `terminal.type`, `user.id`, `tool_use_id`, `user_prompt*`, `interaction.*`, `gen_ai.system`, event `gen_ai.request.attempt`.

- [ ] **Step 1: Register conformance (failing)**

```go
func TestClaudeConformance(t *testing.T) {
	canonical.Conformance(t, canonical.Edge{
		Name:      "claude",
		LoadRaw:   loadNativeFixtureTraces("testdata/"), // parse claude native fixture file
		Normalize: normalizeViaNormalizer,                // feed through New(...) into a sink consumer
		Signals: []canonical.Signal{
			{RawKey: "input_tokens", CanonicalKey: "gen_ai.usage.input_tokens", Kind: canonical.Sum},
			{RawKey: "output_tokens", CanonicalKey: "gen_ai.usage.output_tokens", Kind: canonical.Sum},
			{RawKey: "cache_read_tokens", CanonicalKey: "gen_ai.usage.cache_read.input_tokens", Kind: canonical.Sum},
			{RawKey: "cache_creation_tokens", CanonicalKey: "gen_ai.usage.cache_creation.input_tokens", Kind: canonical.Sum},
			{RawKey: "ttft_ms", CanonicalKey: "gen_ai.response.time_to_first_chunk", Kind: canonical.Presence},
			{RawKey: "stop_reason", CanonicalKey: "gen_ai.response.finish_reasons", Kind: canonical.Presence},
		},
	})
}
```

Run: `go test ./internal/claude/ -run Conformance -v` → Expected: FAIL (usage sums short, ttft absent, vendor keys present).

- [ ] **Step 2: Rewrite normalizer** — build canonical attribute sets explicitly (follow opencode/normalizer.go's copyWireStrings/copyUsage pattern); apply `content.Strip` to ALL claimed spans including native ones; keep hierarchy/reparenting logic untouched.

- [ ] **Step 3: Update unit tests** — passthrough-pinning tests (`TestClaudeTraceNormalizerKeepsSubToolOnlyBatch` etc.) flip to assert remapped keys present, raw keys absent.

- [ ] **Step 4: Harness doc** — create `docs/harnesses/claude-code.md` with the matrix template from the spec; rows for every raw key above with status mapped/dropped/not-provided (no total-token or reasoning source exists upstream — mark those canonical keys "not provided").

- [ ] **Step 5: Green + commit**

Run: `cd connector/codingagentconnector && go test ./internal/claude/ ./internal/canonical/ -v` → PASS

```bash
git add connector/codingagentconnector/internal/claude docs/harnesses/claude-code.md
git commit -m "claude: explicit canonical remap (usage tokens, TTFT, finish reasons)"
```

---

### Task 5: OpenCode edge — chat-span usage, totals, reasoning, TTFT

**Files:** Modify `internal/opencode/normalizer.go`, `normalizer_test.go`; Create `conformance_test.go`, `docs/harnesses/opencode.md`

**Mappings added (doStream → chat):** call `copyUsage` in the doStream branch; passthrough already-canonical `gen_ai.usage.input_tokens`/`output_tokens`; `ai.usage.totalTokens`→`gen_ai.usage.total_tokens`; `ai.usage.reasoningTokens` (or `ai.usage.outputTokenDetails.reasoningTokens`)→`gen_ai.usage.reasoning.output_tokens`; `ai.response.msToFirstChunk`→`gen_ai.response.time_to_first_chunk` (double ms — coerce per existing Int64Value-style semantics, document units as ms).

- [ ] **Step 1:** Register conformance with signals `{ai.usage.inputTokens→input,sum}, {ai.usage.outputTokens→output,sum}, {ai.usage.totalTokens→total,sum}, {gen_ai.usage.input_tokens→input,sum}` (this last pair catches the chat-drop bug via sum equality), `{ai.response.msToFirstChunk→ttft,presence}`. Run → FAIL.
- [ ] **Step 2:** Implement mappings in the doStream branch; nothing else changes.
- [ ] **Step 3:** Extend unit tests: chat span asserts all five token keys + ttft; invoke_agent unchanged.
- [ ] **Step 4:** Write `docs/harnesses/opencode.md` matrix (include dropped: `ai.usage.inputTokenDetails.*`, `ai.telemetry.metadata.userId`, `gen_ai.response.id` deliberate-drop stays).
- [ ] **Step 5:** `go test ./internal/opencode/ -v` → PASS; commit `opencode: map usage/tokens/ttft onto chat spans`.

---

### Task 6: GenAI edge — explicit usage handling, underscore remaps

**Files:** Modify `internal/genai/normalizer.go`, `normalizer_test.go`, testdata; Create `conformance_test.go`, `docs/harnesses/genai-scopes.md` (covers openai_v2, util-genai, strands, copilot emitters)

**Mappings:** in `normalizeSpan`, explicitly enumerate allowed usage keys and REMAP Strands' underscore variants before strip: `gen_ai.usage.cache_read_input_tokens`→`...cache_read.input_tokens`; `gen_ai.usage.cache_write_input_tokens`→`...cache_creation.input_tokens`. Keep legacy prompt/completion dedupe. Everything else already passes/fails via the Task-3 allowlist.

- [ ] **Step 1:** Conformance registration per emitter fixture (strands + openai-adhoc + copilot-cli): signals for input/output/cache sums per fixture contents; run → FAIL where underscore variants still land unremapped (they now violate the allowlist).
- [ ] **Step 2:** Implement remap table in normalizeSpan.
- [ ] **Step 3:** Unit tests: strands fixture replay asserts dotted cache keys, no underscore keys; copilot asserts `reasoning.output_tokens` survives; assert absence of `github.copilot.*`, `event_loop.*`, `enduser.pseudo.id`.
- [ ] **Step 4:** Write `docs/harnesses/genai-scopes.md` with one matrix section per emitter.
- [ ] **Step 5:** Green; commit `genai: explicit usage remapping, drop vendor extensions`.

---

### Task 7: Codex edge — totals, reasoning placement, TTFT; drop turn/tool extras

**Files:** Modify `internal/codex/trace.go`, `metrics_test.go` siblings as needed; Create `conformance_test.go`, `docs/harnesses/codex.md`

**Mappings:** `tokenUsageAttrs` gains `tool_token_count`→`gen_ai.usage.total_tokens` and `reasoning_token_count`→`gen_ai.usage.reasoning.output_tokens` (replacing `coding_agent.usage.reasoning_tokens`); chat span gains `ttft_ms`→`gen_ai.response.time_to_first_chunk`. **Dropped writes:** `coding_agent.turn.{finish_reason,complete,prompt_observed,events_truncated}`, `coding_agent.tool.call_id/success`, decision attrs, `coding_agent.model_provider`, root-event `error.message` (record all as dropped rows in the doc; tool success still sets span Status Error internally — that behavior stays, only the attribute goes).

- [ ] **Step 1:** Conformance: signals `{input_token_count→input,sum}, {output_token_count→output,sum}, {cached_token_count→cache_read,sum}, {tool_token_count→total,sum}, {reasoning_token_count→reasoning,sum}, {ttft_ms→ttft,presence}`; run → FAIL (total/reasoning/ttft missing).
- [ ] **Step 2:** Implement; delete dropped writes; update status-setting code paths so behavior (Error status on failed tools/timeouts) is preserved without the attributes.
- [ ] **Step 3:** Update trace/metrics tests accordingly.
- [ ] **Step 4:** `docs/harnesses/codex.md` matrix incl. dropped keys and the note that finish reasons here were connector-derived finalization reasons, not model stop reasons.
- [ ] **Step 5:** Green; commit `codex: complete usage mapping (total/reasoning/ttft), drop vendor extras`.

---

### Task 8: Cursor edge — drop vendor detail, keep usage core

**Files:** Modify `internal/cursor/trace.go`, tests; Create `conformance_test.go`, `docs/harnesses/cursor.md`

**Changes:** remove writes of all `coding_agent.cursor.*` except none (entire namespace goes; surface/team/user/billable/correction/skill/hook/cloud-agent payloads become dropped rows). Event NAMES survive (`api_correction_*` bodies remain readable as event names — note this in the doc). Keep four usage sums, model, conversation id, provenance quartet.

- [ ] **Step 1:** Conformance: signals for the four usage pairs (sum) — these already pass; the failing assertion will be Allowed-tier violations from surviving `coding_agent.cursor.*` keys → run → FAIL.
- [ ] **Step 2:** Delete the dropped writes; correction/error join logic keeps working at the event-name level.
- [ ] **Step 3:** Update tests asserting cursor-detail attrs → assert absence.
- [ ] **Step 4:** `docs/harnesses/cursor.md` matrix; note no durations exist upstream ⇒ no TTFT row ("not provided").
- [ ] **Step 5:** Green; commit `cursor: restrict canonical output to GenAI vocabulary`.

---

### Task 9: OpenHands edge — provider name, total tokens, drops

**Files:** Modify `internal/openhands/normalizer.go`, tests; Create `conformance_test.go`, `docs/harnesses/openhands.md`

**Mappings:** `gen_ai.system`→`gen_ai.provider.name` (same pattern as genai edge); `llm.usage.total_tokens`→`gen_ai.usage.total_tokens` (delete the "derivable" comment and its absence assertion). **Dropped:** `enduser.pseudo.id`, `coding_agent.openhands.*` (tags, delegate linkage).

- [ ] **Step 1:** Conformance: signals `{gen_ai.usage.input_tokens→input,sum}, {gen_ai.usage.output_tokens→output,sum}, {llm.usage.total_tokens→total,sum}`; run → FAIL (provider absent on system-carrying fixture? add fixture attr if missing; total missing).
- [ ] **Step 2:** Implement; remove dropped writes.
- [ ] **Step 3:** Update tests (`TestNoLaminarBookkeepingInOutput` extended with new absences).
- [ ] **Step 4:** `docs/harnesses/openhands.md` matrix.
- [ ] **Step 5:** Green; commit `openhands: map provider/total-tokens, drop vendor linkage`.

---

### Task 10: Pi edge — finish reasons, response id, raw-leftover cleanup

**Files:** Modify `internal/pi/normalizer.go`, tests; Create `conformance_test.go`, `docs/harnesses/pi.md`

**Mappings:** `stopReason`→`gen_ai.response.finish_reasons` (single-element slice); `responseId`→`gen_ai.response.id`. After mapping, DELETE raw leftovers instead of leaving them: `model`, `provider`, `sessionId`, `durationMs`, `llmGenerationId`, `responseId`, `eventType`, `toolName`, `toolCallId` (add to the post-map removal alongside existing usage-key removal). Cost stays dropped (doc row).

- [ ] **Step 1:** Conformance: signals `{usage.input→input,sum}, {usage.output→output,sum}, {usage.total_tokens→total,sum}, {usage.cache_read→cache_read,sum}, {usage.cache_write→cache_creation,sum}, {stopReason→finish_reasons,presence}`; run → FAIL (finish reasons absent).
- [ ] **Step 2:** Implement mappings + leftover deletion.
- [ ] **Step 3:** Update tests (fixture-replay canonical captures regenerate; assert leftovers gone).
- [ ] **Step 4:** `docs/harnesses/pi.md` matrix (TTFT row: not provided).
- [ ] **Step 5:** Green; commit `pi: map stop reason/response id, purge raw leftovers`.

---

### Task 11: Cross-harness registry, docs, policy, example branch

**Files:**
- Create: `connector/codingagentconnector/conformance_test.go` (module root)
- Create: `docs/canonical-attributes.md`
- Modify: `AGENTS.md` (policy line), `docs/design.md`, `README.md`, `docs/harnesses.md` (links)
- Modify: `examples/otelcol-s3.yaml` (comment documenting it as the raw-preservation branch pattern)

- [ ] **Step 1: Registry test** — module-root test importing all seven internal packages; builds the full pipeline via factory constructors over each registered fixture; hardcodes the seven harness names; asserts (a) each has a passing conformance check, (b) the set matches the edges wired in `traces.go`/`logs.go` (iterate wiring; any wired edge without an entry fails with "register conformance"). Run → PASS once Tasks 4–10 landed.

- [ ] **Step 2: Shared doc** — write `docs/canonical-attributes.md`: the Global Constraints vocabulary list verbatim, marked subset-of upstream (link https://opentelemetry.io/docs/specs/semconv/gen-ai/), the policy sentence ("every harness MUST remap ALL attributes into canonical form; pass-through is not permitted"), pointer to `examples/otelcol-s3.yaml` for raw preservation, links to the seven harness matrices.

- [ ] **Step 3: Policy line** — append to AGENTS.md Layout/workflow section: canonical-alignment rule referencing `docs/canonical-attributes.md`; mirror one paragraph in design.md; link from README and harnesses.md.

- [ ] **Step 4: Example annotation** — comment block atop `examples/otelcol-s3.yaml`: use as a pre-normalization branch to retain original OTLP for case-by-case extraction of dropped vendor detail.

- [ ] **Step 5: Full verification**

Run: `./scripts/check.sh`
Expected: PASS (gofmt, lint v2.11.4 both modules, mdatagen fresh, race tests, compose checks, goreleaser).

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/conformance_test.go docs AGENTS.md README.md examples
git commit -m "canonical: cross-harness conformance registry, canonical-attributes doc, policy"
```

---

## Self-review notes

- Spec coverage: policy+rename (T2), vocabulary single-source (T1/T3), seven edge fixes (T4–T10), conformance interface + mandatory registration (T1/T11), per-harness matrix docs (T4–T10), shared doc + upstream link + example branch (T11). All spec sections have tasks.
- Type consistency: `Edge`/`Signal`/`Check`/`Conformance` signatures defined once in T1 and reused verbatim everywhere.
- Known risk: captured canonical fixtures may drift beyond the listed files; executors should treat fixture-replay failures as authoritative and update captures minimally, never raw inputs.
