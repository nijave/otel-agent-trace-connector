# OpenCode native-trace normalization

Status: approved design, not yet implemented. When implemented, the durable
parts of this document move into `docs/design.md`, which tracks the current
system rather than proposals.

## Goal

Extend the traces-to-traces edge so the canonical pipeline covers OpenCode's
native OpenTelemetry output (`sst/opencode`, `experimental.openTelemetry`):
rename the Vercel AI SDK spans into the canonical `invoke_agent`/`chat`/
`execute_tool` vocabulary and drop OpenCode's internal Effect spans, so an
OpenCode session produces canonical traces like Claude Code does today.

Decisions fixed during design review:

- Native telemetry only. The `felixti/opencode-otel-plugin` (GenAI semconv)
  and `@devtheops/opencode-plugin-otel` (OpenInference) plugin surfaces are
  out of scope; a plugin surface can be added later as a separate claiming
  rule if it shows up in real use.
- Stateless span rewrite on the traces edge, mirroring `internal/claude` —
  no correlation state. Hierarchy, trace/span IDs, kinds, timestamps, and
  status pass through; only names and attributes change.
- Per-step granularity: every `ai.streamText` span becomes one
  `invoke_agent opencode` root. A session that ran N model steps yields N
  canonical roots inside the same downstream trace (the wire keeps one
  TraceId per session); consumers already tolerate multiple roots per trace
  (the Codex validator's `firstValidRoot` pattern).
- Live E2E included: a new opt-in stack drives real `opencode run` against
  the collector, like the Codex/Claude stacks.
- Unit-test fixtures are captured from live e2e runs (the stack already
  writes `.e2e-output/raw-traces.json`), not from backend exports.

Non-goals: metrics or logs edges for OpenCode (native OTel is
traces-focused), Effect-span normalization, repo/VCS identity enrichment
(native telemetry carries none), Kilo Code fork support.

## Research basis

Verified against live traffic in this deployment's ClickHouse
(`ServiceName=opencode`, scope `opencode`, client 1.18.21) on 2026-08-22;
cross-checked with the harness research in `docs/harnesses.md`. The surface
is upstream's dev branch made stable; it is additive, so the normalizer must
tolerate unknown attributes and span names by dropping rather than guessing.

Resource: `service.name=opencode`; resource attributes carry only runtime
environment (k8s/container when containerized). No cwd, repo, branch, or
remote URL. Instrumentation scope: name `opencode`, version = client version.

Every LLM step exports one subtree under a long-lived per-session trace
(one TraceId holds many steps):

| Span | Attributes observed |
| --- | --- |
| `ai.streamText` | `session.id` (`ses_…`), `ai.model.provider`, `ai.model.id`, `gen_ai.request.model`, `gen_ai.system`, `gen_ai.request.max_tokens`, full usage — `ai.usage.inputTokens`, `ai.usage.outputTokens`, `ai.usage.totalTokens`, `ai.usage.reasoningTokens`, `ai.usage.cachedInputTokens`, `ai.usage.inputTokenDetails.cacheReadTokens`, `ai.usage.inputTokenDetails.noCacheTokens`, `ai.usage.outputTokenDetails.textTokens`, `ai.usage.outputTokenDetails.reasoningTokens` — plus content-bearing `ai.response.text` |
| `ai.streamText.doStream` (child, one per model request within the step) | `gen_ai.response.id`, `gen_ai.response.model`, `gen_ai.response.finish_reasons`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` |
| `ai.toolCall` (children of `ai.streamText`) | `ai.toolCall.id`, `ai.toolCall.name` (`bash`, `read`, `edit`, MCP names, …), content-bearing `ai.toolCall.args`, `ai.toolCall.result`, `session.id` |

Everything else on the wire is internal Effect instrumentation — `sql.execute`,
`Config.get`, `SessionProcessor.*`, `FileSystem.*`, `Plugin.trigger`,
`Tool.execute`, `Snapshot.*`, hundreds of spans per step — and never enters
canonical output. The raw pipeline passes all of it through untouched, as for
every other source.

Consequences that shape the design:

1. The AI SDK already parents tool calls and per-request spans to the
   `ai.streamText` root, so no synthesis or re-parenting is needed; renaming
   in place preserves the tree exactly like the Claude edge does.
2. Spans can fragment across exports (a child may land in an export without
   its ancestor — the same fact documented on the Claude edge), so the
   normalizer must stay stateless and rewrite whatever each batch contains,
   letting backends reassemble by preserved IDs.
3. Usage lives in two places: whole-step totals (`ai.usage.*`, including
   cache and reasoning detail) on the root, per-request sums
   (`gen_ai.usage.*`) on `doStream`. Both map onto existing canonical keys.
4. Content (`ai.response.text`, `ai.toolCall.args/result`) sits on exactly
   the three claimed span types; dropping unclaimed spans plus copying only
   an attribute allowlist onto renamed spans removes it structurally, not by
   denylisting keys.

## Design

### Claiming

New package `connector/codingagentconnector/internal/opencode/` exposing
`New(next consumer.Traces) connector.Traces` and
`ContainsOpenCodeSpans(ptrace.ResourceSpans) bool`, wired into
`tracesRouter` alongside the Claude and GenAI edges. A resource group is
claimed iff any scope is named exactly `opencode` (exact match, unlike the
GenAI prefix rules — `opencode.`-prefixed scopes belong to plugins/Kilo and
must not match). Claims are disjoint from Claude (span-name prefix) and
GenAI (scope prefixes) by construction; no deferral is required.

### Span mapping

Inside claimed groups, rename by exact span name; drop every other span from
canonical output:

| Wire span | Canonical | Added attributes |
| --- | --- | --- |
| `ai.streamText` | `invoke_agent opencode` | `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name=opencode`, `gen_ai.conversation.id` ← `session.id` (span first, then resource), usage totals mapped per the table below |
| `ai.streamText.doStream` | `chat {model}` | `gen_ai.operation.name=chat`; name subject ← the span's own `gen_ai.request.model` (present on every span observed); when absent, emit the bare name `chat` |
| `ai.toolCall` | `execute_tool {tool}` | `gen_ai.operation.name=execute_tool`, `gen_ai.tool.name` ← `ai.toolCall.name`; `ai.toolCall.id` has no canonical home in the current vocabulary and is dropped rather than inventing a key |

All renamed spans also get the common marker set used by the other edges:
`telemetry.source=native`, `coding_agent.client.name=opencode`,
`coding_agent.client.version` ← `service.version` resource attribute, and
`coding_agent.source.event` ← original wire span name (mirrors
`internal/claude`). `gen_ai.provider.name` is left absent when the wire has
no usable value (`gen_ai.system` on this wire holds the SDK name, not a
provider, and is not copied).

Usage mapping onto the connector's existing canonical vocabulary (same keys
the Codex edge emits):

| Wire (`ai.streamText`) | Canonical |
| --- | --- |
| `ai.usage.inputTokens` | `gen_ai.usage.input_tokens` |
| `ai.usage.outputTokens` | `gen_ai.usage.output_tokens` |
| `ai.usage.cachedInputTokens` | `gen_ai.usage.cache_read.input_tokens` |

Reasoning and token-detail counters have no established canonical key yet;
they are dropped from canonical output rather than inventing vocabulary here.

### Attribute policy

Renamed spans are rebuilt with only the attributes named in the mapping
table plus the common marker set — wire attributes are never copied
wholesale. This makes content removal structural (`ai.response.text`,
`ai.toolCall.args`, `ai.toolCall.result` have no path into canonical
output) instead of relying on a denylist that silently fails open as the
wire evolves. Dropping unclaimed Effect spans keeps noise attributes such
as `db.query.text` out the same way. The raw pipeline continues to export
everything unchanged.

### Error and edge handling

- Status passes through untouched (`StatusCode`, message).
- Steps missing usage attributes (in-flight or failed steps) emit with those
  attributes simply absent; no zero-filling.
- `ai.toolCall` spans whose parent is not a claimed `ai.streamText` still
  normalize — parentage is the backend's concern, matching how fragmented
  exports are handled everywhere else on this edge.
- Batches containing no claimed group produce no output (no empty fan-out),
  identical to the Claude/GenAI edges.

### Live E2E

New opt-in stack following the shared pattern:

- `compose.e2e-opencode.yaml`: includes `compose.e2e-base.yaml`; defines one
  `agent` service built from `e2e/opencode/Dockerfile` (Node image, pinned
  `opencode-ai` npm package ≥ 1.18). The agent gets the z.ai key as
  `OPENAI_API_KEY` configured as an OpenAI-compatible provider pointed at
  z.ai's coding endpoint, `OTEL_EXPORTER_OTLP_ENDPOINT` at the collector,
  and OpenCode config enabling `experimental.openTelemetry`.
- `e2e/opencode/run.sh` drives non-interactive `opencode run` with one
  harmless Bash-tool call, mirroring the Claude runner's guardrails
  (`timeout`, single allowed tool).
- `scripts/e2e-opencode.sh` mirrors `scripts/e2e-claude.sh`:
  `compose_files=(-f compose.e2e-opencode.yaml)`,
  `support_services=(collector)`, `e2e_run opencode`.
- Validator: `"opencode"` joins the accepted `E2E_AGENT` values; canonical
  validation requires an `invoke_agent opencode` root carrying
  `gen_ai.conversation.id`, at least one `chat` child, at least one
  `execute_tool bash` child, usage attributes on the root, and — across all
  run spans — absence of content (`rejectSensitiveAttrs` plus a check that
  `ai.toolCall.args` / `ai.response.text` appear on raw but never canonical).

### Fixture capture from e2e runs

Unit-test fixtures come from live runs, not backend queries. After any
successful `scripts/e2e-opencode.sh` run, `.e2e-output/raw-traces.json`
holds the full native wire output for the run. The capture procedure
(documented in `e2e/README.md`): pick one representative resource group,
keep one complete `ai.streamText` step subtree plus a sample of dropped
Effect spans, redact nothing (e2e prompts are harmless by construction),
and store it as
`connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json`.
A replay test feeds the fixture through the normalizer and asserts the
canonical tree, names, mapped attributes, and absence of content — the same
shape as the GenAI edge's raw/canonical fixture pairs.

### Testing

- Unit (unpaid, CI): claiming/disjointness, all three renames, attribute
  allowlist and content absence, usage mapping incl. missing-usage tolerance,
  unknown-span dropping, input-not-mutated guarantee, fixture replay.
- Compose/config validation in `scripts/check.sh`: add the new stack to the
  credential-split loop (only `OPENAI_API_KEY` reaches the agent; no
  Anthropic credential present) and build its image.
- Live (opt-in, paid): `scripts/e2e-opencode.sh`.

### Docs

README harness list gains an OpenCode bullet; `docs/design.md` and the
component README note the new claimed scope and the per-step/per-root
granularity consequence; `e2e/README.md` gains the OpenCode section with the
fixture-capture procedure; `docs/harnesses.md` relevance section notes the
native path is now handled.
