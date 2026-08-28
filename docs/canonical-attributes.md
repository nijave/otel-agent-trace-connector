# Canonical attribute vocabulary

Every span the connector emits on its canonical edge carries attributes from
this list and nothing else. The list is the single source of truth in
[`connector/codingagentconnector/internal/canonical/vocabulary.go`](../connector/codingagentconnector/internal/canonical/vocabulary.go);
this page mirrors it.

Most of the vocabulary comes from the upstream
[OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai),
audited against commit `67dff024` of that repo; [Provenance](#provenance)
records the exact split. Canonical output optimizes for tracking LLM usage,
cost, and performance uniformly across harnesses. Vendor detail outside that
scope is deliberately dropped rather than carried through — see
[Raw preservation](#raw-preservation) for how to recover it.

## Policy

> Every supported harness MUST remap ALL attributes from its raw representation
> into the canonical form. Prefix pass-through is not permitted.

A normalizer may emit only attributes it writes explicitly under a canonical
key. No `gen_ai.usage.` wildcard exists: unknown usage-family keys are vendor
keys and never reach canonical output. Each emitted span must carry the three
required keys: `gen_ai.operation.name`, `coding_agent.source`, and
`coding_agent.client.name`. A cross-harness conformance test
([`connector/codingagentconnector/conformance_test.go`](../connector/codingagentconnector/conformance_test.go))
enforces the contract per harness in CI, and each edge package carries its own
conformance test against a captured native fixture.

### Conditional coverage in the matrices

The per-harness matrices in [docs/harnesses/](harnesses/) record presence,
not just mapping. When a realistic wire condition omits a source field — a
provider that reports no usage, a streamed completion without token counts,
an optional wire attribute — the matrix row must say so inline:
`mapped (absent when …)`, never bare `mapped`. Each harness's headline
coverage caveats (conditional usage, missing tool spans, missing durations)
must appear in its own matrix file even when the README or
[docs/design.md](design.md) also state them; the matrix is where a reader
checks what a harness can answer, and a caveat recorded only elsewhere goes
unread. Derive statuses from the code's presence guards and the pinned
fixtures, not from the happy path alone.

## Resource attributes

Resource attributes follow the same fail-closed rule as span attributes. The
canonical resource vocabulary is the standard OTel identity keys:

- `service.name` (required on every emitted resource group; it feeds
  `coding_agent.client.name`)
- `service.version`
- `telemetry.sdk.name`
- `telemetry.sdk.language`
- `telemetry.sdk.version`

Every other key — vendor resources such as `cursor.surface`, raw keys such as
`session.id` — stays out of canonical output. Edges that consume raw
resource values (for example `session.id` → `gen_ai.conversation.id`) read them
before the strip.

## Vocabulary

`TestVocabularyDocsMirror` keeps the block below identical to
`canonicalAttributeKeys` in
[`connector/codingagentconnector/internal/canonical/vocabulary.go`](../connector/codingagentconnector/internal/canonical/vocabulary.go)
and fails CI on drift. Edit the vocabulary
there, never here. Keys appear in declaration order:

<!-- vocabulary:generated -->
- `gen_ai.operation.name`
- `gen_ai.provider.name`
- `gen_ai.request.model`
- `gen_ai.request.max_tokens`
- `gen_ai.request.stream`
- `gen_ai.request.reasoning.level`
- `gen_ai.response.finish_reasons`
- `gen_ai.response.id`
- `gen_ai.response.model`
- `gen_ai.response.time_to_first_chunk`
- `gen_ai.agent.id`
- `gen_ai.agent.name`
- `gen_ai.agent.version`
- `gen_ai.conversation.id`
- `gen_ai.tool.call.id`
- `gen_ai.tool.name`
- `gen_ai.tool.type`
- `gen_ai.tool.status`
- `gen_ai.event.start_time`
- `gen_ai.event.end_time`
- `gen_ai.usage.input_tokens`
- `gen_ai.usage.output_tokens`
- `gen_ai.usage.total_tokens`
- `gen_ai.usage.cache_read.input_tokens`
- `gen_ai.usage.cache_write.input_tokens`
- `gen_ai.usage.reasoning.output_tokens`
- `server.address`
- `server.port`
- `exception.type`
- `exception.message`
- `exception.escaped`
- `exception.stacktrace`
- `coding_agent.request.service_tier`
- `coding_agent.source`
- `coding_agent.source.scope`
- `coding_agent.source.event`
- `coding_agent.client.name`
- `coding_agent.client.version`
<!-- /vocabulary:generated -->

`gen_ai.response.time_to_first_chunk` is seconds, double — every edge converts its wire unit to seconds at normalization time.

`gen_ai.usage.cache_write.input_tokens` follows the current GenAI registry
name. The pre-rename `gen_ai.usage.cache_creation.input_tokens` spelling still
appears on some wires; the GenAI edge remaps it onto the registry key, and the
other edges map their native cache-write counters onto it directly.

`gen_ai.request.reasoning.level` records the reasoning effort the request asked for (Codex's `model_reasoning_effort`; it corresponds to OpenAI's `reasoning.effort` request parameter), copied verbatim from the wire when present.

`coding_agent.request.service_tier` records the provider service tier a request ran in, copied verbatim from the wire when present. The connector namespace holds it rather than a provider namespace because providers beyond OpenAI have tier concepts and other harnesses pass tiers through; one key keeps cross-provider queries uniform.


Beyond the enumerated keys, any key under the `exception.` prefix may appear:
exception details are standard OTel companions on error spans.

That is the complete list. Everything else — vendor namespaces such as
`github.copilot.*`, `ai.*`, `claude_code.*`, `lmnr.*`, `llm.usage.*`,
`event_loop.*`, `coding_agent.cursor.*`, `coding_agent.openhands.*`, and raw
pass-through leftovers — stays out of canonical output. Nothing disappears
silently: each harness document records every dropped key and where it came
from.

## Provenance

- **GenAI registry**: 22 of the 26 `gen_ai.*` keys above come verbatim from
  the GenAI semantic-conventions registry at commit `67dff024` of
  [open-telemetry/semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai).
- **General OTel semconv**: `server.address`, `server.port`, and the
  `exception.*` keys come from the main
  [open-telemetry/semantic-conventions](https://github.com/open-telemetry/semantic-conventions)
  registry, not the GenAI registry.
- **Connector extensions under `gen_ai.`** — not in the registry, and kept
  spelled as the wire spells them; renaming pass-through keys would add remap
  complexity without a query benefit:
  - `gen_ai.usage.total_tokens`: the registry defines no total-tokens key
    (consumers sum input+output), but five sources report a provider total on
    the wire — Codex `tool_token_count`, OpenHands `llm.usage.total_tokens`,
    OpenCode `ai.usage.totalTokens`, Pi `usage.total_tokens`, and Strands'
    native `gen_ai.usage.total_tokens` on the GenAI edge. A provider total is
    not always input+output, so dropping it would lose information.
  - `gen_ai.tool.status`: Strands emits it natively on execute_tool spans and
    the GenAI edge keeps it verbatim. It is distinct from the registry's
    `gen_ai.response.status`, which tracks response lifecycle.
  - `gen_ai.event.start_time`, `gen_ai.event.end_time`: Strands emits them
    natively on every span and the GenAI edge keeps them verbatim.
- **Connector namespace**: `coding_agent.request.service_tier`,
  `coding_agent.source`, `coding_agent.source.scope`,
  `coding_agent.source.event`, `coding_agent.client.name`, and
  `coding_agent.client.version` are connector-defined.

## Raw preservation

Canonical output is deliberately lossy. Dropped vendor detail is recoverable
only via a raw-preservation pipeline branch: route the original OTLP to storage
before the connector normalizes it, then extract specific fields case by case
downstream. The connector adds no components for this.
[`examples/otelcol-s3.yaml`](../examples/otelcol-s3.yaml) shows the pattern:
parallel raw logs/traces pipelines exporting beside the normalizing connector.

## Per-harness mapping matrices

One document per supported harness records its raw → canonical matrix, with a
status column for mapped / not-provided / dropped-deliberate:

- [Claude Code](harnesses/claude-code.md)
- [Codex](harnesses/codex.md)
- [Cursor](harnesses/cursor.md)
- [GenAI-semconv scopes](harnesses/genai-scopes.md) (openai-v2, util-genai,
  Strands, GitHub Copilot)
- [OpenCode](harnesses/opencode.md)
- [OpenHands](harnesses/openhands.md)
- [Pi](harnesses/pi.md)

[docs/harnesses.md](harnesses.md) remains the upstream research record for what
each harness exports before normalization.
