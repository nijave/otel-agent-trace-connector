# OpenHands SDK native-trace support

Status: approved design, not yet implemented. When implemented, the durable
parts of this document move into `docs/design.md`, which tracks the current
system rather than proposals.

## Goal

Extend the traces-to-traces edge so the canonical pipeline covers OpenHands
(All Hands AI's open-source agent SDK) native OpenTelemetry output by claiming
instrumentation scope `lmnr.tracer` groups that carry OpenHands markers. A new
stateless normalizer package `internal/openhands` maps the SDK's span
vocabulary — `conversation` roots, `litellm.completion` LLM calls, dynamic
tool-name spans — onto the canonical `invoke_agent openhands` → `chat` /
`execute_tool` tree.

Decisions fixed during design review:

- Stateless sibling semantics for delegated subagents. The SDK severs each
  delegate into its own trace linked only by metadata attributes; every
  claimed group becomes its own `invoke_agent openhands` trace carrying the
  same `gen_ai.conversation.id`. Downstream consumers regroup on that id.
  Cross-trace stitching is future work.
- Validation is paid live E2E plus committed fixtures. The live stack mirrors
  `e2e/pi`: headless SDK script with a real LLM key, opt-in, capturing raw and
  canonical OTLP fixtures that feed unpaid unit and validator tests.
- Vendor bookkeeping stays out of canonical output. All `lmnr.span.*`
  attributes are internal Laminar machinery; only the association properties
  this design names enter canonical output.
- Non-goals: OTLP logs and metrics signals (OpenHands emits neither over
  OTLP), delegate stitching, cost attributes (the wire carries none), and the
  packaged desktop/docker UI path (it does not forward `OTEL_*` env today).

## Research basis

Verified 2026-08-23 against source, not docs alone:
`OpenHands/software-agent-sdk` at `9421149` (SDK 1.43.1), the `lmnr` wheel
0.7.56 it pins, and the `OpenHands/OpenHands` frontend repo at `3487bb1`.
The full findings live in the session research notes; the facts below shape
the design.

Enablement: tracing activates when any of `LMNR_PROJECT_API_KEY`,
`OTEL_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, or
`OTEL_EXPORTER_OTLP_ENDPOINT` is set before the process imports the SDK.
Default exporter is OTLP gRPC to Laminar cloud; generic collectors need
`OTEL_EXPORTER=otlp_http` or `..._PROTOCOL=http/protobuf`, and `OTEL_*`
endpoint variables are honored only when `LMNR_PROJECT_API_KEY` is unset.
Traces only: no meter or logger scope exists anywhere in the stack.

Instrumentation scope: every span carries tracer name `lmnr.tracer`. That
name is shared by any Laminar-instrumented application, so scope alone cannot
claim — the marker rule below supplies OpenHands specificity. Resource
attributes are effectively `service.name = sys.argv[0]` and nothing else; the
raw `Resource` constructor skips standard env detection, so no client version
or repo identity exists on the wire.

Span vocabulary (all INTERNAL kind):

| Span name | Role |
| --- | --- |
| `conversation` | long-lived root, one per conversation |
| `conversation.send_message` / `.run` / `.arun` / `.ask_agent` / `.generate_title` | structural request lifecycle |
| `agent.step` / `.astep`, `acp_agent.step` / `.astep` | agent loop iterations |
| `<tool_name>` (dynamic), `MCPToolExecutor.call_tool`, record-result spans | tool executions, TOOL type |
| `litellm.completion` / `litellm.responses`, `acp.completion` | LLM calls |

Correlation: `lmnr.association.properties.session_id` = conversation UUID,
stamped as a span attribute on every span in the conversation. Delegate
sub-conversations run under a detached context, producing severed sibling
traces whose spans carry `lmnr.association.properties.metadata.*` linkage:
`is_delegate=true`, `task_id`, `subagent_type`, `parent_session_id`,
`delegate.parent_trace_id`, `delegate.parent_span_id`, `tool_call_id`, and
tags `["delegate"]`.

Usage accounting lives on the LLM spans:
`gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`,
`llm.usage.total_tokens`, `gen_ai.usage.cache_read_input_tokens`,
`gen_ai.usage.cache_creation_input_tokens` (cache keys only when provider
details exist). No cost attributes and no reasoning tokens are emitted.
Streamed completions set response attributes but **no usage attributes at
all** — a documented gap upstream, not a connector concern to paper over.

Content is rich by default: full prompts including system prompt in
`gen_ai.input.messages`, assistant output and tool-call arguments in
`gen_ai.output.messages`, tool schemas in `gen_ai.tool.definitions`, function
args/returns in `lmnr.span.input` / `lmnr.span.output`, plus
`gen_ai.request.base_url` and response fingerprints. No redaction switch
exists short of disabling tracing.

Churn risk is high: the RootSpan rework after a 60% parent-loss bug, recent
delegate-detachment changes, a brand-new ACP tracing module, and an lmnr pin
of `<0.8` all signal movement. The stable elements are the session-id
attribute and the core `gen_ai.usage.*` set; fixtures get re-verified against
source when either moves.

## Design

### Claiming

A resource-group/scope pair is claimed when the instrumentation scope name is
exactly `lmnr.tracer` **and** the group carries an OpenHands marker:

- any span named in the SDK's conversation or agent families (`conversation`,
  `conversation.send_message`, `conversation.run`, `conversation.arun`,
  `agent.step`, `agent.astep`, `acp_agent.step`, `acp_agent.astep`), or
- any span with `lmnr.association.properties.metadata.is_delegate=true`.

The marker requirement keeps other Laminar-instrumented applications
unclaimed; their `@observe` span names derive from function names and will
not match the fixed vocabulary. Disjointness holds against the existing
edges: none claims scope `lmnr.tracer` today, and the Claude check keys on
span-name prefixes absent here. The exact marker set gets pinned against the
captured live fixture during implementation.

### Canonical mapping

| Wire span | Becomes |
| --- | --- |
| `conversation` (root) | `invoke_agent openhands` root |
| `litellm.completion` / `litellm.responses` / `acp.completion` | `chat <model>` child |
| TOOL spans (`<tool_name>`, `MCPToolExecutor.call_tool`) | `execute_tool <name>` child |
| everything else (structural list above) | dropped; timing folds into root bounds |

Root: preserves the conversation span's trace and span IDs (same ID policy as
the opencode edge), start/end cover the first-to-last claimed span timing,
and attributes:

- `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name=openhands`,
  `gen_ai.conversation.id` from the session-id association property.
- `coding_agent.client.name=openhands`, `coding_agent.source=native`,
  `coding_agent.source.scope=lmnr.tracer`. No client version exists on the
  wire; none is invented.
- `enduser.pseudo.id` from the user-id association property when present.
- Operator-set labels copied by allowlist: keyed `conversation.tags.<key>`
  resource-of-conversation labels land under `coding_agent.openhands.tag.<key>`;
  the tags association property (a string list) lands under
  `coding_agent.openhands.tags`.
- Delegate linkage evidence lands under
  `coding_agent.openhands.delegate.*` (`true`, `task_id`, `subagent_type`,
  `parent_session_id`, `tool_call_id`) so sibling fragments stay reconcilable
  downstream without stitching.

Chat children: name `chat <model>` using the model attribute the LLM spans
carry (`gen_ai.request.model` expected; bare `chat` when absent, verified
against the captured fixture), point-preserving start/end from the wire span,
per-span usage mapped from the five accounting keys above;
`llm.usage.total_tokens` is derivable and not copied. Streamed calls emit
spans without usage; those spans simply carry no usage attributes.

Tool children: named `execute_tool <tool_name>` with `gen_ai.tool.name`;
spans carrying `metadata.tool_call_id` dedupe on that id within the group
(the SDK can record a call and its result as separate TOOL spans).

Attribute policy is an allowlist: the builder copies only the fields named
above. Content-bearing attributes (`gen_ai.input.messages`,
`gen_ai.output.messages`, `gen_ai.system_instructions`,
`gen_ai.tool.definitions`, `gen_ai.request.base_url`, response fingerprints,
every `lmnr.span.*` key except the consumed association properties) never
reach canonical output. Span events (Laminar exception records) are not
copied; error status codes pass through.

### Testing

Unit (unpaid, CI), mirroring the cursor/opencode suites:

- Claiming: marked groups claim; unmarked `lmnr.tracer` groups and foreign
  scopes do not; existing edges' precedence unchanged.
- Mapping: conversation → root with preserved IDs; LLM spans → chat with
  usage variants (full, cache-less, streaming-no-usage); tool spans →
  execute_tool with dedupe on `tool_call_id`; structural spans dropped but
  folded into root bounds.
- Delegate siblings: two groups sharing one session id normalize
  independently, both carrying the conversation id, one flagged delegate.
- Stripping: content attributes and events never reach output; validator
  rejects them over the canonical fixture.
- Fixture replay: sanitized raw OTLP fixture captured from the live stack,
  replayed through the edge, matches the committed canonical fixture.

Live E2E (opt-in, paid): `e2e/openhands` Compose stack running the real SDK
headless against a real LLM key, shaped like `e2e/pi`, exporting through the
collector under test and capturing both fixtures.

## Known limitations and future work

- Streamed completions carry no token usage upstream; per-call usage for
  streaming conversations is absent until the SDK closes the gap.
- Cost never appears on the wire; consumers needing cost join elsewhere.
- Delegate traces stay siblings; stitching them into one canonical tree would
  require cross-trace state and ordering machinery a stateless edge
  deliberately lacks.
- The packaged desktop/docker UI does not forward `OTEL_*` env; export works
  when running the SDK or agent-server directly. Revisit if upstream fixes it.
- Upstream churn (lmnr `<0.8` pin, ACP module) may move names; refresh the
  marker set and fixtures against source before relying on new fields.
- Repo/workspace identity is absent upstream; the launch-time
  `OTEL_RESOURCE_ATTRIBUTES` convention does not apply because the SDK skips
  standard resource detection. Documented, not worked around.

## Docs

README harness list gains an OpenHands bullet; `docs/design.md`
implemented-sources list gains "OpenHands SDK native-trace normalization";
`docs/harnesses.md` relevance section moves OpenHands from unsorted to
handled, noting the claiming markers and the streamed-usage gap.
