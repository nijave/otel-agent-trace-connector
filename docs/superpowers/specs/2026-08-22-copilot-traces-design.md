# GitHub Copilot native-trace support via the GenAI edge

Status: approved design, not yet implemented. When implemented, the durable
parts of this document move into `docs/design.md`, which tracks the current
system rather than proposals.

## Goal

Extend the traces-to-traces edge so the canonical pipeline covers GitHub
Copilot's native OpenTelemetry output (Copilot CLI and the VS Code Copilot
Chat extension) by claiming instrumentation scope `github.copilot` inside the
existing GenAI-semconv normalizer. Copilot emits exactly the span vocabulary
that normalizer already handles — `invoke_agent` → `chat` / `execute_tool`
with `gen_ai.*` attributes — so an edge that took a dedicated package for
OpenCode is here a one-line claiming change plus tests.

Decisions fixed during design review:

- Route inside the GenAI edge; no new package. Copilot spans need no mapping
  the shared normalizer does not already perform.
- No configurable scope allowlist in this round. `COPILOT_OTEL_SOURCE_NAME`
  can rename the producer scope, but a config knob to chase it is speculative
  until someone actually renames it; the default name claims, custom names do
  not, and that limitation is documented.
- Vendor attributes pass through untouched. `github.copilot.cost`,
  `github.copilot.aiu`, `github.copilot.turn_id`, `.interaction_id`,
  `.turn_count`, `.server_duration`, `.initiator` have no canonical keys
  today; inventing vocabulary is out of scope (same rule as the OpenCode
  design). The GenAI edge copies claimed groups rather than rebuilding
  attribute maps, so pass-through is free.
- VS Code specifics need no code: `execute_hook` operations are absent from
  the rename table so those spans keep their wire names and still gain the
  marker set; legacy `copilot_chat.repo.*` attributes ride along untouched
  (upstream dual-emits both namespaces permanently); lifecycle span events
  (`github.copilot.session.shutdown` totals and friends) are not content and
  pass through.
- Fixtures are authored from the source-verified wire schema, as the Cursor
  edge did. A live E2E stack waits until someone with a paid Copilot
  subscription validates non-interactive CLI invocation; unit fixtures do not
  depend on it.
- Non-goals: metrics and logs signals (traces edge only), the cloud coding
  agent (no OTLP export; its hook path shares one scope across eight agents,
  cannot route Copilot by scope, and carries no token counts), JetBrains
  extension telemetry (none found).

## Research basis

Verified 2026-08-22 against primary sources: the CLI OTel reference
(`content/copilot/reference/copilot-cli-reference/cli-command-reference.md`
in `github/docs`), the `github/copilot-cli` changelog (OTel GA since v1.0.4,
2026-03-11), the VS Code monitoring guide (`microsoft/vscode-copilot-chat`,
`docs/monitoring/agent_monitoring.md`; rendered at
code.visualstudio.com/docs/agents/guides/monitoring-agents), the SDK
observability doc (`github/copilot-sdk`), and the
`o11y-dev/opentelemetry-hooks` source tree. The facts below shape the design.

Enablement: CLI exports when any of `COPILOT_OTEL_ENABLED=true`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, or `COPILOT_OTEL_FILE_EXPORTER_PATH` is set;
VS Code adds settings-based activation. Instrumentation scope: name
`github.copilot` on tracer and meter (default of `COPILOT_OTEL_SOURCE_NAME`);
resource `service.name` defaults to `github-copilot` (CLI) or `copilot-chat`
(VS Code). Signals: CLI = traces + metrics only (lifecycle facts arrive as
span events); VS Code = traces + metrics + events as OTel logs.

Span tree: top-level sessions and subagent invocations are both INTERNAL
`invoke_agent` spans carrying `gen_ai.operation.name=invoke_agent`,
`gen_ai.agent.id/name/description/version`, `gen_ai.conversation.id` (session
UUID), `enduser.pseudo.id`, model/request/response attrs, and usage totals.
Children: CLIENT `chat` spans (`gen_ai.conversation.id`, per-call usage,
TTFT) and INTERNAL `execute_tool` spans (`gen_ai.tool.name/type/call.id`).
Usage keys are the semconv spellings fixed upstream in v1.0.64:
`gen_ai.usage.input_tokens`, `output_tokens`, `cache_read.input_tokens`,
`cache_creation.input_tokens`; reasoning tokens appear per changelog but are
absent from the reference table — expect either
`gen_ai.usage.reasoning.output_tokens` or a legacy underscore variant.

Content capture exists behind opt-in flags and lands in attributes the
normalizer already strips: `gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.system_instructions`, `gen_ai.tool.definitions`,
`gen_ai.tool.call.arguments/result`. All are members of the existing
`contentAttributeKeys` deny-by-construction list.

VS Code adds a third operation, `execute_hook`, plus git identity on
`invoke_agent` (`github.copilot.git.repository/.branch/.commit_sha/
.github.org`). The CLI emits no repo identity at all — workspace attribution
for pure-CLI sessions stays with whoever launches the binary (documented
`OTEL_RESOURCE_ATTRIBUTES` convention, same answer as every other harness
with this gap).

## Design

### Claiming

Add one entry to `scopePrefixes` in
`connector/codingagentconnector/internal/genai/normalizer.go`:

```go
"github.copilot",
```

Prefix matching matches the existing mechanism and tolerates hypothetical
sub-scopes; nothing known today lives under `github.copilot.*`. Disjointness
holds by construction: the Claude check (span-name prefix) and the OpenCode
check (scope `opencode`) run first and neither claims groups whose scopes are
`github.copilot`. Conversely, Claude Code subprocess telemetry forwarded from
VS Code arrives under Claude scopes and belongs to the Claude edge exactly as
it does today.

### What the shared normalizer already does

No mapping changes are required. For claimed groups the existing code path:

| Wire span | Becomes | Subject attribute |
| --- | --- | --- |
| `invoke_agent` op | `invoke_agent {agent}` | `gen_ai.agent.name` |
| `chat` op | `chat {model}` | `gen_ai.request.model` |
| `execute_tool` op | `execute_tool {tool}` | `gen_ai.tool.name` |

and additionally derives `gen_ai.provider.name` from `gen_ai.system` when
absent, strips content attributes/events, applies legacy token-key mapping,
and stamps `telemetry.source=native`, `coding_agent.source.scope`,
`coding_agent.client.name` ← resource `service.name`, and
`coding_agent.client.version`.

### Pass-through policy

Attributes without canonical homes stay on renamed spans verbatim: the vendor
cost/AIU/correlation extras, VS Code git identity, and both namespaces of the
permanent dual emission. Span events survive intact — they carry lifecycle
totals, not message content. Renamed-span names for unknown operations (e.g.
`execute_hook`) fall back to the wire name; the marker set still applies.

Multi-service stitching needs no work: a VS Code wrapper span (service
`copilot-chat`) parenting CLI native spans (service `github-copilot`) splits
into two resource groups, each claimed independently, hierarchy preserved by
untouched trace/span IDs. Copilot-hosted Claude sessions (`invoke_agent claude`
under scope `github.copilot`) normalize like any other `invoke_agent` span.

### Testing

Unit (unpaid, CI):

- Claiming: a `github.copilot` group is claimed; a group with only foreign
  scopes is not; Claude/OpenCode precedence unchanged.
- Renames: all three operations with subjects present; subject missing → bare
  operation name retained per existing behavior; `execute_hook` keeps its
  wire name.
- Content stripping: capture-gated attributes and events never reach output.
- Markers and provider derivation on each span type.
- Pass-through: vendor extras, git identity, dual namespaces, span events.
- Fixture replay: committed OTLP fixture built from the documented schema,
  covering the CLI flavor (span events, vendor extras) and the VS Code flavor
  (`execute_hook`, git identity).

Live E2E: deferred (see future work).

## Known limitations and future work

- A renamed producer scope (`COPILOT_OTEL_SOURCE_NAME`) does not claim. If
  real users rename, revisit the configurable allowlist then.
- Reasoning-token key unconfirmed against a live capture (docs/changelog
  disagree); whichever spelling appears passes through unmapped today.
- Cost/premium-request *metrics* do not exist upstream yet (open issues);
  cost lives on spans as pass-through attributes.
- Live E2E stack deferred pending subscription access; add
  `compose.e2e-copilot.yaml` + runner mirroring the OpenCode stack when
  feasible, with fixture capture following `e2e/README.md`.
- Repo identity for pure-CLI sessions is absent upstream; document the
  launch-time `OTEL_RESOURCE_ATTRIBUTES` convention in the README when this
  ships.

## Docs

README harness list gains a Copilot bullet; `docs/design.md` implemented-
sources list gains "GitHub Copilot native-span normalization (via the GenAI
edge)" with the claimed scope noted; `docs/harnesses.md` relevance section
moves Copilot from "pending confirmation" to handled, noting the hooks path
stays unsupported.
