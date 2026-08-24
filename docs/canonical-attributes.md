# Canonical attribute vocabulary

Every span the connector emits on its canonical edge carries attributes from
this list and nothing else. The list is the single source of truth in
[`connector/codingagentconnector/internal/canonical/vocabulary.go`](../connector/codingagentconnector/internal/canonical/vocabulary.go);
this page mirrors it.

The vocabulary is a **subset** of the upstream
[OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/):
canonical output optimizes for tracking LLM usage, cost, and performance
uniformly across harnesses. Vendor detail outside that scope is deliberately
dropped rather than carried through — see [Raw preservation](#raw-preservation)
for how to recover it.

## Policy

> Every supported harness MUST remap ALL attributes from its raw representation
> into the canonical form. Prefix pass-through is not permitted.

A normalizer may emit only attributes it writes explicitly under a canonical
key. There is no `gen_ai.usage.` wildcard: unknown usage-family keys are vendor
keys and never reach canonical output. Each emitted span must carry the three
required keys: `gen_ai.operation.name`, `coding_agent.source`, and
`coding_agent.client.name`. A cross-harness conformance test
([`connector/codingagentconnector/conformance_test.go`](../connector/codingagentconnector/conformance_test.go))
enforces the contract per harness in CI, and each edge package carries its own
conformance test against a captured native fixture.

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
`session.id` — is stripped from canonical output. Edges that consume raw
resource values (for example `session.id` → `gen_ai.conversation.id`) read them
before the strip.

## Vocabulary

Connector-owned provenance namespace:

- `coding_agent.source`
- `coding_agent.source.scope`
- `coding_agent.source.event`
- `coding_agent.client.name`
- `coding_agent.client.version`

Operation, request, and response:

- `gen_ai.operation.name`
- `gen_ai.provider.name`
- `gen_ai.request.model`
- `gen_ai.request.max_tokens`
- `gen_ai.request.stream`
- `gen_ai.response.finish_reasons`
- `gen_ai.response.id`
- `gen_ai.response.model`
- `gen_ai.response.time_to_first_chunk`
- `gen_ai.server.time_to_first_token`

Agent, conversation, and tool:

- `gen_ai.agent.id`
- `gen_ai.agent.name`
- `gen_ai.agent.version`
- `gen_ai.conversation.id`
- `gen_ai.tool.call.id`
- `gen_ai.tool.name`
- `gen_ai.tool.type`
- `gen_ai.tool.status`

Timing and server identity:

- `gen_ai.event.start_time`
- `gen_ai.event.end_time`
- `server.address`
- `server.port`

Usage (enumerated explicitly; there is no `gen_ai.usage.` prefix exemption):

- `gen_ai.usage.input_tokens`
- `gen_ai.usage.output_tokens`
- `gen_ai.usage.total_tokens`
- `gen_ai.usage.cache_read.input_tokens`
- `gen_ai.usage.cache_creation.input_tokens`
- `gen_ai.usage.reasoning.output_tokens`

Exceptions (standard OTel companions on error spans):

- `exception.type`, `exception.message`, `exception.escaped`,
  `exception.stacktrace` (any key under the `exception.` prefix)

That is the complete list. Everything else — vendor namespaces such as
`github.copilot.*`, `ai.*`, `claude_code.*`, `lmnr.*`, `llm.usage.*`,
`event_loop.*`, `coding_agent.cursor.*`, `coding_agent.openhands.*`, and raw
pass-through leftovers — is stripped from canonical output. Nothing disappears
silently: each harness document records every dropped key and where it came
from.

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
