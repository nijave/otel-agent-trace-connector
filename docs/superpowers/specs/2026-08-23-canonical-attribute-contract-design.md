# Canonical attribute contract — design

Date: 2026-08-23
Status: draft for review

## Problem

Every supported harness must supply its telemetry in the canonical form: all
attributes remapped, key by key, from the harness's raw representation into the
canonical vocabulary. Prefix pass-through is not an accepted implementation.

**Intentional trade-off.** The primary concern is tracking LLM usage, cost,
and performance. Canonical output optimizes for that: uniform token counts,
TTFT, model identity, and latency across every harness. Vendor detail outside
that scope (billing hooks, correction kinds, delegate linkage) is deliberately
dropped from canonical output rather than carried through — losing it from the
canonical view is accepted, and recoverable via a raw-preservation pipeline
branch when a use case appears.

Today several edges violate this:

- **Claude** never strips or remaps native spans; every operational signal
  (`input_tokens`, `ttft_ms`, `stop_reason`, …) stays under vendor names.
- **OpenCode** drops ready-made `gen_ai.usage.input/output_tokens` on chat
  spans, plus `totalTokens`, reasoning tokens, and `msToFirstChunk`.
- **GenAI scopes** survive on the `gen_ai.usage.` allowlist prefix instead of
  explicit mappings.
- **Codex** puts reasoning tokens outside the GenAI usage family and drops
  `tool_token_count` (total) and `ttft_ms`.
- **OpenHands** drops `gen_ai.system` (provider) and `llm.usage.total_tokens`.
- **Pi** strips `stopReason` despite the finish-reason slot existing.
- Time-to-first-token is mapped nowhere except where copilot/strands happen to
  pass it through.

Nothing in CI enforces any of this, so drift recurs silently.

## Policy

1. **Remap everything.** A normalizer may emit only attributes it writes
   explicitly under a canonical key. Pass-through of raw attributes into
   canonical output is forbidden.
2. **Canonical aligns 1:1 with upstream.** The [OTel GenAI semantic
   conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/) are the
   source of truth for which keys *may* exist. Canonical output carries
   `gen_ai.*` keys plus standard OTel companions (`server.*`, `exception.*`)
   and the connector-owned `coding_agent.*` provenance namespace — nothing
   else. Vendor namespaces (`github.copilot.*`, `ai.*`, `claude_code.*`,
   `lmnr.*`, `llm.usage.*`, …) never reach canonical output.
3. **Raw signal is preserved outside the canonical path when needed.** If a
   use case requires vendor detail the canonical form drops (billing hooks,
   correction kinds, delegate linkage), the collector configuration routes the
   original traces/logs to storage before normalization; extraction happens
   case-by-case downstream. The connector adds no machinery for this beyond an
   optional documented pipeline branch.
4. **Repo docs define emitted.** The repo tracks what *is* emitted today: one
   shared canonical list plus one document per harness containing its
   raw→canonical mapping matrix. Docs, code, and tests must agree; CI fails on
   drift.

## Shared canonical vocabulary

Harness-neutral keys, enforced at runtime by `internal/content` (the existing
allowlist, minus the prefix exemption):

- Provenance (connector-owned namespace): `coding_agent.source` (renamed from
  `coding_agent.source`; values `native`/`normalized`),
  `coding_agent.source.scope`, `coding_agent.source.event`,
  `coding_agent.client.name`, `coding_agent.client.version`
- Operation/request/response: `gen_ai.operation.name`, `gen_ai.provider.name`,
  `gen_ai.request.model`, `gen_ai.request.max_tokens`,
  `gen_ai.request.stream`, `gen_ai.response.finish_reasons`,
  `gen_ai.response.id`, `gen_ai.response.model`,
  `gen_ai.response.time_to_first_chunk`, `gen_ai.server.time_to_first_token`
- Agent/conversation/tool: `gen_ai.agent.{id,name,version}`,
  `gen_ai.conversation.id`, `gen_ai.tool.{call.id,name,type,status}`
- Timing/server: `gen_ai.event.{start_time,end_time}`, `server.address`,
  `server.port`
- Usage (explicitly enumerated; **the `gen_ai.usage.` prefix exemption is
  deleted**): `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`,
  `gen_ai.usage.total_tokens`, `gen_ai.usage.cache_read.input_tokens`,
  `gen_ai.usage.cache_creation.input_tokens`,
  `gen_ai.usage.reasoning.output_tokens`
- Exceptions: standard `exception.{type,message,escaped,stacktrace}`

That is the complete list. Everything else — including today's
`github.copilot.*`, `event_loop.*`, `coding_agent.cursor.*`,
`coding_agent.openhands.*`, `coding_agent.turn.*`, `coding_agent.tool.*`,
`enduser.pseudo.id`, and raw passthrough leftovers — is dropped from canonical
output. The per-harness documents record each dropped key so nothing
disappears silently.

Key decisions:

- Reasoning tokens use `gen_ai.usage.reasoning.output_tokens`, matching what
  Copilot emits natively; Codex's `coding_agent.usage.reasoning_tokens` moves
  onto it.
- Client-observed TTFT maps to `gen_ai.response.time_to_first_chunk`;
  `gen_ai.server.time_to_first_token` remains reserved for server-reported
  values (Strands today).

## Raw preservation

Canonical output is deliberately lossy. The escape valve is configuration, not
code: the collector pipeline may branch original OTLP to storage (file, S3, or
a second backend) before the connector normalizes it. `examples/` gains a
commented example of such a branch; the per-harness matrix documents mark
every dropped key with its raw source so a case-by-case extraction knows what
to look for. The connector itself grows no new components for this.

## Contract package: `internal/canonical`

Contract as data, three tiers:

1. **Required** — on every emitted canonical span:
   `gen_ai.operation.name`, `coding_agent.source`, `coding_agent.client.name`.
   No per-harness exceptions.
2. **Source-backed signals** — declared per edge as
   `{RawKey, CanonicalKey, Kind}` with `Kind ∈ {sum, presence}`:
   - Token counters use `sum`: the total of the canonical key across output
     must equal the total of the raw equivalent(s) across native input. Catches
     partial mapping (OpenCode chat) and total absence (Claude).
   - Latency/one-offs (`ttft_ms`, finish reasons) use `presence`: raw signal
     anywhere ⇒ canonical key somewhere.
   - Nothing declared ⇒ nothing enforced, so sources genuinely lacking a
     signal (Cursor logs carry no durations) need no waivers.
3. **Forbidden** — enforced positively, not by denylist: every attribute on a
   canonical span or surviving event must be in the shared vocabulary above —
   enumerated `gen_ai.*` keys including the six enumerated usage keys,
   `server.address`/`server.port`, `exception.*`, and the five
   `coding_agent.*` provenance keys. No `gen_ai.usage.` wildcard exists;
   unknown usage keys fail like any other vendor key.

Runner API:

```go
type Signal struct {
    RawKey       string // native fixture attribute/log field
    CanonicalKey string // required counterpart in output
    Kind         SignalKind // sum | presence
}

type Edge struct {
    Name      string
    Normalize func() (ptrace.Traces, error) // replay native fixture through the full edge pipeline
    Signals   []Signal
}

func Conformance(t *testing.T, e Edge)
```

Each edge package adds a thin `conformance_test.go` registering its existing
native testdata fixture. Adding a harness without registering conformance is
impossible: the runner enumerates registered edges from the connector factory
tests, and an unregistered supported harness fails CI.

## Normalizer fixes (per-edge atomic commits)

Ordered worst-first:

1. **Claude** — remap `input_tokens/output_tokens/cache_read_tokens/
   cache_creation_tokens` → `gen_ai.usage.*`; `ttft_ms` →
   `gen_ai.response.time_to_first_chunk`; `stop_reason` →
   `gen_ai.response.finish_reasons`; apply explicit remap + strip to native
   spans instead of passthrough-plus; drop stale `gen_ai.system`.
2. **OpenCode** — copy usage onto chat spans; add `ai.usage.totalTokens` →
   `gen_ai.usage.total_tokens`, reasoning →
   `gen_ai.usage.reasoning.output_tokens`, `ai.response.msToFirstChunk` →
   `gen_ai.response.time_to_first_chunk`.
3. **GenAI edge** — replace prefix reliance with explicit usage-key handling;
   drop `github.copilot.*`, `event_loop.*`, `enduser.pseudo.id` from output;
   refresh stale captured fixtures that still contain keys the current
   allowlist rejects (`gen_ai.tool.description`, `gen_ai.tool.json_schema`,
   some `github.copilot.context.*`).
4. **Codex** — `tool_token_count` → `gen_ai.usage.total_tokens`;
   `reasoning_token_count` → `gen_ai.usage.reasoning.output_tokens`;
   `ttft_ms` → `gen_ai.response.time_to_first_chunk`; drop
   `coding_agent.turn.*`, `coding_agent.tool.*`, `coding_agent.model_provider`
   from span attributes (recorded as dropped keys in the harness doc).
5. **Cursor** — drop `coding_agent.cursor.*` detail keys (surface, team.id,
   billable, correction kinds, skill/hook/cloud-agent payloads) from canonical
   spans; keep the four usage mappings, model, and conversation id.
6. **OpenHands** — `gen_ai.system` → `gen_ai.provider.name`;
   `llm.usage.total_tokens` → `gen_ai.usage.total_tokens`; drop
   `enduser.pseudo.id`, tags, and delegate linkage.
7. **Pi** — `stopReason` → `gen_ai.response.finish_reasons`; `responseId` →
   `gen_ai.response.id`; remove raw leftovers (`model`, `provider`,
   `sessionId`, `durationMs`, `llmGenerationId`, `eventType`, …) after
   mapping.

## Documentation structure

Two layers:

- `docs/canonical-attributes.md` — the emitted shared vocabulary, marked as a
  subset of the upstream OTel GenAI semconv (linked). States the policy, notes
  that dropped vendor detail is recoverable only from a raw-preservation
  pipeline branch (with a pointer to the example), and links each harness
  document.
- `docs/harnesses/<name>.md` — one per implemented harness, each containing
  the matrix: raw attribute/log field → canonical key, per span type, with a
  status column (mapped / not provided by source / dropped-deliberate).
  Template:

  | Raw key | Span type | Canonical key | Status |

  The existing `docs/harnesses.md` remains the upstream research record and
  links to these documents.

## Testing

- `internal/canonical` holds the runner; unit-tested with a synthetic edge.
- Each edge gains `conformance_test.go`; all seven must pass before the
  policy lands in AGENTS.md.
- `internal/content` loses the prefix exemption; its tests update accordingly.
- Fixture-replay tests already present per edge keep guarding byte-level
  shape; conformance adds the cross-harness guarantee.

## Non-goals

- No runtime contract validation beyond the existing allowlist strip — the
  hot path stays simple; the contract is enforced at build/test time.
- No new connector components for raw preservation — that is a collector
  pipeline branch, documented with an example, not code here.
- No changes to claiming logic or scope selection.
- Metrics edges are untouched; this covers traces/logs-to-traces output only.
