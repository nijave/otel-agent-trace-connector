# GenAI semconv trace normalization: openai-v2, util-genai, and Strands

Status: approved design, not yet implemented. When implemented, the durable
parts of this document move into `docs/design.md`, which tracks the current
system rather than proposals.

## Goal

Extend the traces-to-traces edge so the canonical pipeline covers spans that
ad-hoc Python agents commonly emit:

1. `opentelemetry-instrumentation-openai-v2` (the official OTel contrib
   instrumentation for the OpenAI Python SDK), in both of its emission modes.
2. Spans that hand-rolled agents emit directly through
   `opentelemetry-util-genai` (`chat`, `invoke_agent`, `execute_tool`,
   `invoke_workflow`).
3. Strands Agents SDK (Python) native traces.

Decisions fixed during design review:

- Stateless pass-through, exactly like the Claude edge: preserve IDs and
  hierarchy, never synthesize spans. Traces without an `invoke_agent` root
  stay rootless.
- The normalizer strips content-bearing span attributes and events from
  canonical output. Full fidelity remains in the parallel raw pipeline.
- Two new opt-in live E2Es (openai-v2 ad-hoc agent and Strands agent), both
  against z.ai's OpenAI-compatible endpoint.

Non-goals: turn correlation or root synthesis for rootless traces (recorded
as future work), the OpenAI Agents SDK, and generic acceptance of arbitrary
GenAI-semconv emitters beyond the detection rules below.

## Research basis

This section reflects primary sources as of 2026-08-19.

`opentelemetry-instrumentation-openai-v2` wraps the OpenAI Python SDK (the
SDK itself has no built-in OTel tracing; openai/openai-python#2276 requested
it and the maintainers pointed at instrumentation packages). It has two
modes:

- Default mode, pinned to semconv v1.30.0. Instrumentation scope
  `opentelemetry.instrumentation.openai_v2`. CLIENT spans named
  `chat {model}` and `embeddings {model}` with `gen_ai.operation.name`,
  `gen_ai.system=openai`, `gen_ai.request.model`, request parameters,
  `server.address`/`server.port`, and response-side `gen_ai.response.model`,
  `gen_ai.response.id`, `gen_ai.response.finish_reasons`,
  `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`. Prompt and
  completion content goes to log events only (a different signal), off by
  default.
- Experimental mode (`OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental`),
  semconv v1.37.0. The `opentelemetry-util-genai` package emits the spans
  (scope `opentelemetry.util.genai.handler`), uses
  `gen_ai.provider.name=openai`, adds Responses API coverage
  (`openai.api.type`), and can place content on span attributes
  (`gen_ai.input.messages`, `gen_ai.output.messages`) when capture is
  enabled. `opentelemetry-util-genai` also exposes inference, embedding,
  tool, workflow, and local/remote agent invocations, so hand-rolled agents
  can emit the full `invoke_agent`/`chat`/`execute_tool` vocabulary through
  the same scope.

Upstream is renaming the package to
`opentelemetry-instrumentation-genai-openai` in the new
`opentelemetry-python-genai` repository, which will change its scope name.

Strands Agents SDK has a built-in tracer (`strands.telemetry.tracer` scope,
from `strands-py/src/strands/telemetry/tracer.py`). Span names are already
semconv-shaped in both convention modes: `invoke_agent {agent_name}`,
`execute_event_loop_cycle`, `chat` (no model suffix; model in
`gen_ai.request.model`), `execute_tool {tool_name}`, `invoke_swarm`/
`invoke_graph`, and `memory.*`. The convention switch mirrors openai-v2:
default emits `gen_ai.system=strands-agents`, latest-experimental emits
`gen_ai.provider.name=strands-agents`. Strands emits token usage under both
legacy (`gen_ai.usage.prompt_tokens`/`completion_tokens`) and current
(`input_tokens`/`output_tokens`) keys plus `total_tokens` and cache
read/write counts. Strands captures content by default as span events
(`gen_ai.user.message`, `gen_ai.choice`, `gen_ai.system.message` legacy;
`gen_ai.client.inference.operation.details` carrying
`gen_ai.input.messages`/`gen_ai.output.messages` latest); the
`gen_ai_span_attributes_only` token copies that content onto span
attributes, and execute_tool spans under latest conventions record
`gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` as span
attributes. Redaction is opt-in via the
`gen_ai_unredacted_attributes=<list>` token; absent the token, Strands
exports content unredacted.

Primary sources:

- [opentelemetry-instrumentation-openai-v2](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2)
- [opentelemetry-util-genai handler](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/util/opentelemetry-util-genai)
- [Strands Agents traces documentation](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
- [Strands tracer source](https://github.com/strands-agents/sdk-python)
- [GenAI semconv spans](https://opentelemetry.io/docs/specs/semconv/gen-ai/)

Provider schemas are pre-1.0 and unstable; rerun fixtures and E2Es before
bumping pinned client versions (existing repo policy).

## Architecture

A new stateless package `internal/genai` joins `internal/codex` and
`internal/claude`. The traces-to-traces edge, currently constructed as
`claude.New(next)` in `factory.go`, becomes a small router that assigns each
resource-spans group to at most one normalizer:

1. Group contains a `claude_code.`-prefixed span: Claude normalizer,
   unchanged behavior.
2. Otherwise, group contains a scope-spans block whose instrumentation-scope
   name matches the GenAI allowlist: GenAI normalizer.
3. Otherwise the group is not part of the canonical edge output. It remains
   available in the parallel raw pipeline.

Claude-first claiming means a resource group is never emitted twice and
existing Claude deployments see no behavior change. Like the Claude edge,
the GenAI normalizer copies the whole claimed resource group, including
spans from the application's own tracer scopes, so an ad-hoc agent's manual
parent spans keep their children and trace parentage stays intact.

`Capabilities()` reports `MutatesData: false`; the normalizer copies the
input batch before modification. No state, no timers, no new configuration:
`Config` remains the Codex alias, and existing pipeline wiring picks up the
new sources automatically.

## Detection

Scope-name matching, evaluated per scope-spans block:

| Rule | Matches |
| --- | --- |
| `opentelemetry.instrumentation.openai_v2` (prefix) | openai-v2 default mode |
| `opentelemetry.util.genai` (prefix) | openai-v2 experimental mode and direct util-genai users |
| `opentelemetry.instrumentation.genai` (prefix) | the announced upstream package rename |
| `strands.telemetry` (prefix) | Strands Agents SDK built-in tracer |

Within a claimed group, the normalizer rewrites a span only when its scope
matched and it carries `gen_ai.operation.name`. Everything else in the group
passes through untouched, mirroring how the Claude edge leaves
`claude_code.tool.execution` and hook spans alone.

Accepted consequence: this edge would claim future official OTel GenAI
instrumentations, since they will share the
`opentelemetry.instrumentation.genai` prefix. They use the same vocabulary,
so claiming them beats breaking on the upstream rename. A configurable
allowlist extension is future work, not part of this design.

## Normalization

Span names conform to the canonical `{operation} {subject}` shape. The
normalizer rewrites the name only when the subject attribute is present;
otherwise it keeps the emitted name:

| `gen_ai.operation.name` | Canonical name | Subject attribute |
| --- | --- | --- |
| `chat` | `chat {model}` | `gen_ai.request.model` |
| `invoke_agent` | `invoke_agent {agent}` | `gen_ai.agent.name` |
| `execute_tool` | `execute_tool {tool}` | `gen_ai.tool.name` |

openai-v2 already emits `chat {model}`; Strands emits bare `chat` and gains
the suffix. Operations outside this table (`embeddings`,
`invoke_workflow`, `execute_event_loop_cycle`, `memory.*`, multiagent
operations) keep their emitted names and hierarchy.

Attribute handling on normalized spans:

- `gen_ai.provider.name`: kept if present; otherwise the normalizer copies
  the value from legacy `gen_ai.system`, and it removes `gen_ai.system` from
  canonical output in both cases. Values stay as emitted (`openai`,
  `strands-agents`); the connector does not guess the upstream model
  provider, consistent with the existing `gen_ai.provider.name` stance in
  `docs/design.md`. Strands names the framework, not the model provider;
  the known-limitations section records this and the connector preserves
  the value.
- Token usage: when `gen_ai.usage.input_tokens` is absent and
  `gen_ai.usage.prompt_tokens` is present, the normalizer copies the value
  (same for output/completion). It removes legacy
  `prompt_tokens`/`completion_tokens` keys from canonical output either
  way. `total_tokens` and cache read/write counts pass through unchanged.
- Provenance: `coding_agent.source=native`,
  `coding_agent.source.scope=<original instrumentation scope name>` (the
  GenAI analog of `coding_agent.source.event`),
  `coding_agent.client.name` from resource `service.name` and
  `coding_agent.client.version` from `service.version` when present. An
  ad-hoc agent's identity is its service, not a known client binary.
- Everything else passes through: `server.address`/`server.port`, request
  parameters, `gen_ai.response.*`, `gen_ai.conversation.id` when an agent
  sets it, span kind, status, IDs, and links.

## Content stripping

Applied to every span in a claimed group (content keys only appear on GenAI
spans, so this is a cheap blanket rule). The lists cover current emitters
plus known older Strands layouts.

Span attributes removed:

- `gen_ai.input.messages`, `gen_ai.output.messages`,
  `gen_ai.input.messages.ref`, `gen_ai.output.messages.ref`
- `gen_ai.system_instructions`, `system_prompt`
- `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`
- `gen_ai.tool.definitions`, `gen_ai.agent.tools` (bulk tool schemas;
  potentially sensitive, available in raw)
- `gen_ai.user.message`, `gen_ai.assistant.message`,
  `gen_ai.system.message`, `gen_ai.tool.message`, `gen_ai.choice`,
  `gen_ai.choice.message`, `gen_ai.choice.tool.result` (older Strands
  attribute layouts and the `gen_ai_span_attributes_only` mode)

Span events removed entirely (with their attributes):

- `gen_ai.client.inference.operation.details`
- `gen_ai.user.message`, `gen_ai.assistant.message`,
  `gen_ai.system.message`, `gen_ai.tool.message`, `gen_ai.choice`
- `memory.query`, `memory.content`

openai-v2's default mode sends content to log events, which never enter this
edge, so stripping matters for Strands defaults and openai-v2 experimental
`span_only`/`span_and_event` capture modes.

## Privacy

Unlike Codex and Claude Code, where content requires enabling explicit
gates, Strands exports prompt and completion content by default and its
redaction is opt-in. Under default agent settings the raw traces destination
receives content. The README privacy section gains a recommendation:
configure Strands redaction (`gen_ai_unredacted_attributes` token) or apply
raw-pipeline access policy accordingly. Canonical output is clean
regardless, per the stripping rules above.

## Configuration and component surface

No new configuration fields, no new component type, no `metadata.yaml`
changes, and no new self-observability metrics (the Claude edge has none
either; per-source normalization counters are future work). The
traces-to-traces edge keeps its existing development stability.

## Testing

Unit tests in `internal/genai`, table-driven like the Claude normalizer
tests, using fixture batches handcrafted from the documented schemas:

- openai-v2 default-mode `chat {model}` CLIENT span; assert name kept,
  `gen_ai.system` mapped to `gen_ai.provider.name` and removed, usage kept.
- openai-v2 experimental / util-genai `chat` span with content attributes
  present; assert content attributes removed.
- util-genai `invoke_agent`, `execute_tool`, and `invoke_workflow` spans;
  assert naming rules and pass-through of `invoke_workflow`.
- Full Strands tree (`invoke_agent` root, `execute_event_loop_cycle`,
  `chat`, `execute_tool` with default content events and duplicate token
  keys); assert model suffix added to `chat`, legacy token keys mapped and
  removed, content events removed, cycle and memory spans untouched.
- Copy-on-write: input batch unmutated; hierarchy, IDs, kinds, and status
  preserved; resource groups without matching scopes not emitted.

Router tests: Claude-first claiming; mixed batches holding Claude, GenAI,
and unknown resource groups in one payload; a resource group containing both
application-scope spans and openai-v2-scope spans keeps both. The
`e2e/validator` package gains assertion helpers for the two new shapes.

Live E2Es, opt-in and paid, following the per-source convention (own compose
file per stack, shared `compose.e2e-base.yaml` collector, exactly one
credential per stack, CI builds and validates but never runs them):

- `e2e/openai-adhoc`: pinned Python image with pinned `openai`,
  `opentelemetry-instrumentation-openai-v2`, SDK, and OTLP exporter
  packages. A small ad-hoc agent script makes one chat-completions call to
  z.ai's OpenAI-compatible endpoint (no responses-proxy needed, unlike
  Codex) and force-flushes. The script runs twice in one container run, once
  per convention mode via `OTEL_SEMCONV_STABILITY_OPT_IN`, under distinct
  service names, so one paid run validates both modes. The runner sets the
  run marker through `OTEL_RESOURCE_ATTRIBUTES`, which the Python SDK honors
  (the collector-side marker used for Codex exists only because Codex
  ignores that variable). Validation: normalized rootless `chat` spans for
  both modes, provider `openai`, usage tokens, no content keys.
- `e2e/strands`: pinned `strands-agents` with its OpenAI-compatible model
  provider pointed at z.ai, one harmless tool the prompt forces. Validation:
  untouched native tree in raw output, normalized
  `invoke_agent`/`chat`/`execute_tool` in canonical output, content present
  in raw and absent in canonical.

## Documentation updates

- Root and connector READMEs: new sources listed, detection table, the
  Strands privacy note, and updated example pipeline comments stating the
  traces edge handles Claude Code, openai-v2/util-genai, and Strands.
  Example instance names stay the same; `coding_agent/claude` keeps working.
- `docs/design.md`: a new GenAI semconv normalization section recording the
  research basis, claiming rules, and the decisions above, added when the
  implementation lands.

## Known limitations and future work

- No turn grouping or root synthesis for rootless ad-hoc traces (explicit
  decision). Opt-in `invoke_agent` synthesis behind a config flag is future
  work if rootless traces prove common.
- Upstream scope names are pre-1.0 and will change with the announced
  package rename; prefix matching mitigates, and rerun fixtures and E2Es
  before bumping pins.
- Strands sets `gen_ai.provider.name=strands-agents` (framework, not model
  provider); the connector preserves the value as emitted.
- A configurable scope allowlist extension is future work.
- Captured sanitized OTLP fixtures from live runs (an existing follow-up
  noted in `docs/design.md`) would extend the handcrafted unit fixtures
  here.
