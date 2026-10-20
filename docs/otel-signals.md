# OTel signal support across coding-agent harnesses

This document answers one question directly, for every coding-agent harness
covered by this repository's research: does the harness natively support
OpenTelemetry **traces**, **logs**, and **metrics**? It sits above
[docs/harnesses.md](harnesses.md) (scoped to token usage and project/repo
identity) and [docs/metrics.md](metrics.md) (the metrics instrument catalog)
as the top-level per-signal matrix; those two files remain the detailed
record for the concerns they cover, and this file leans on their research
rather than repeating it.

"Native" means the harness or its own SDK can put the signal on the wire
without a third-party plugin or extension; "via extension" / "via plugin"
marks signals that exist only through unofficial add-ons. Provider telemetry
surfaces are not stable APIs; treat this as a snapshot, not a contract.

Research refreshed 2026-08-29, closing gaps the 2026-08-20/21 research behind
`docs/harnesses.md` and `docs/metrics.md` left open because that research
targeted token usage, repo identity, and metrics instrument detail, not a
full signal matrix: Claude Code's logs signal (real and long-standing, but
outside that research's scope), GitHub Copilot's logs and metrics coverage
(that research stopped at traces), Pi's per-extension logs support, and
Strands' logs signal. The findings below check against primary sources
(official docs, SDK source) as of this date.

On 2026-10-19 every supported harness gained an audited per-signal reference
under [docs/harnesses/](harnesses/) with pinned citations; the Codex, Claude
Code, OpenCode, and Pi findings below now reflect those audits (checked
2026-10-19), which supersede the 2026-08 dates where they differ.

## Summary

| Harness | Traces | Logs | Metrics | Connector edge |
| --- | --- | --- | --- | --- |
| **Claude Code** | native (beta, `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`) | native (`OTEL_LOGS_EXPORTER`) | native (8 counters) | traces |
| **Codex** | native, opt-in (`otel.trace_exporter`, 0.163.0) | native (`codex.*` events) | native (counters + histograms) | logs (synthesizes traces) |
| **Cursor** | none native (hook tooling only) | native (Enterprise beta) | native (Enterprise beta, 3 delta sums) | logs (synthesizes traces) |
| **GitHub Copilot** (CLI + VS Code Chat) | native | VS Code Chat only, as unbadged "events"; CLI: none | native (both surfaces) | traces (GenAI edge) |
| **OpenCode** | native since v1.4.5 (2026-04-15) | native (Effect logs) | none native (plugins only) | traces |
| **Pi** | via extension only (no built-in exporter) | via extension only — one of six surveyed | via extension only | traces (`@amaster.ai/pi-telemetry` only) |
| **OpenHands** | native (via Laminar SDK) | none | none exported (in-process only) | traces |
| **Cline** | none ("distributed tracing not yet implemented") | native | native, degraded on `main` (see below) | not supported |
| **Kilo** | native (token content stripped) | native | none | not supported |
| **Hermes** | native, gateway-health scope only | native, same scope limit | native, same scope limit (16 gauges) | not supported |
| **Strands Agents SDK** | native | none | native | traces (GenAI edge) |
| **openai-v2 / util-genai** | native | opt-in content-capture log-event mode | native (GenAI-semconv histograms) | traces (GenAI edge) |

## Claude Code

All three signals are native and independently switchable — see
[the CLI's monitoring docs](https://code.claude.com/docs/en/monitoring-usage)
and the [Agent SDK observability docs](https://code.claude.com/docs/en/agent-sdk/observability),
which state plainly: "The CLI exports three independent OpenTelemetry
signals. Each has its own enable switch and its own exporter."

- **Traces** (beta): `OTEL_TRACES_EXPORTER` plus
  `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`. Spans for each interaction, model
  request, and tool call (hook spans only under detailed beta tracing) —
  this is the signal
  [docs/harnesses/claude-code.md](harnesses/claude-code.md) maps.
- **Logs**: `OTEL_LOGS_EXPORTER`. Structured events
  (`claude_code.user_prompt`, `claude_code.tool_result`,
  `claude_code.tool_decision`, `claude_code.api_request`,
  `claude_code.api_error`, `claude_code.mcp_server_connection`,
  `claude_code.permission_mode_changed`, …) — long-standing, not tied to the
  traces beta. `docs/harnesses.md`'s summary row reads "native traces (beta)
  + native logs", pointing here for the full signal inventory; the full
  logs story lives in [docs/harnesses/claude-code.md](harnesses/claude-code.md)
  (checked 2026-10-19).
- **Metrics**: `OTEL_METRICS_EXPORTER`. Eight counters under meter
  `com.anthropic.claude_code` — `claude_code.session.count`,
  `claude_code.lines_of_code.count`, `claude_code.pull_request.count`,
  `claude_code.commit.count`, `claude_code.cost.usage`,
  `claude_code.token.usage`, `claude_code.code_edit_tool.decision`,
  `claude_code.active_time.total` (delta temporality by default) — catalogued
  in [docs/metrics.md](metrics.md) and
  [docs/harnesses/claude-code.md](harnesses/claude-code.md) (checked
  2026-10-19).

## GitHub Copilot

CLI and VS Code Chat diverge on logs. Sources: the
[Copilot CLI reference's OpenTelemetry section](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring)
and [VS Code's agent monitoring guide](https://code.visualstudio.com/docs/agents/guides/monitoring-agents).

- **Traces**: native on both, `github.copilot` scope
  (`invoke_agent`/`chat`/`execute_tool`) — already covered in
  [docs/harnesses/genai-scopes.md](harnesses/genai-scopes.md).
- **Logs**: Copilot CLI's docs explicitly scope OTel export to "traces and
  metrics" only; it has span events (`github.copilot.hook.start`,
  `exception`) but no standalone OTLP logs signal. VS Code Copilot Chat's
  docs describe a third signal, "events" (`gen_ai.client.inference.operation.details`,
  `copilot_chat.session.start`, …) — the OTel spec implements Events on top
  of the Logs API, but GitHub's docs never call this "logs" or mention
  `OTEL_LOGS_EXPORTER`, so treat it as logs-shaped rather than confirmed
  logs.
- **Metrics**: native on both. Copilot CLI documents GenAI-convention
  histograms (`gen_ai.client.operation.duration`, `gen_ai.client.token.usage`)
  plus vendor counters (`github.copilot.tool.call.count`,
  `github.copilot.code.lines_added`). VS Code Chat documents a full
  counter/histogram table under the same GenAI conventions plus
  `copilot_chat.*` extension metrics.
- **Not OTel**: GitHub's org-level Copilot usage/activity REST and CSV APIs
  (e.g. `last_activity_at`, activity reports —
  [metrics-data docs](https://docs.github.com/en/copilot/reference/metrics-data))
  are a separate, non-OTel channel; don't confuse them with the logs/metrics
  signals above.

## Pi

Pi itself has no built-in OTel exporter. Its own repository ships a
`@earendil-works/pi-telemetry` package (since pi 0.84.0), but that is a
vendor-neutral span contract with "no exporter, global current-span state,
or dependency on a telemetry backend" — nothing reaches an OTLP endpoint
without a third-party npm extension, and each extension picked its own
subset of signals to support. Surveyed (all checked 2026-10-19; full detail
in [docs/harnesses/pi.md](harnesses/pi.md)):

| Extension | Traces | Logs | Metrics |
| --- | --- | --- | --- |
| `@amaster.ai/pi-telemetry` (the one the connector supports) | yes | no | no |
| `pi-otel-telemetry` (mprokopov, auto-discovered by pi from the extensions directory) | yes | no | yes (8 instruments; the common `user.name`/`host.name`/`environment` attrs exist only on repo `main` — npm 1.0.0 records `tool.name` only) |
| `@the-agency/pi-observability` | yes | no | no (0.4.0 depends only on the OTLP trace exporter and trace SDK) |
| `pi-otel` (NikiforovAll) | yes | **yes** (opt-in `PI_OTEL_LOGS=1`; `pi.session.start/end`, `pi.tool.error`, `pi.llm_request.error` as real OTLP LogRecords) | yes (opt-in `PI_OTEL_METRICS=1`: histograms `gen_ai.client.operation.duration`, `gen_ai.client.token.usage`, `gen_ai.client.tool_calls_per_operation` and counter `gen_ai.client.tool.calls`) |
| `maxmalkin/pi-OTEL` | yes | no | yes (counters `gen_ai.client.token.usage`, `gen_ai.client.cost`, `pi.tool.calls`, `pi.session.compactions`, `pi.provider.retries`, `pi.turn.cancellations`; histograms `gen_ai.client.operation.duration`, `pi.tool.duration`) |
| `devkade/pi-opentelemetry` | yes (`src/trace/provider.ts` builds an `OTLPTraceExporter`) | no | yes, confirmed at source level (`src/metrics/collector.ts`/`provider.ts`: a real `MeterProvider` + `OTLPMetricExporter` defining `pi.session.count`, `pi.turn.count`, `pi.tool_call.count`, `pi.tool_result.count`, `pi.prompt.count`, `pi.token.usage`, `pi.cost.usage`, `pi.session.duration`, `pi.turn.duration`, `pi.tool.duration`) |

`pi-otel` (NikiforovAll) and `senad-d/ObservMe` (below) are the only
surveyed extensions with a genuine OTLP logs signal; source:
https://nikiforovall.blog/pi-otel/configuration. The connector claims spans
from a different extension (`@amaster.ai/pi-telemetry`), so neither logs
signal is part of the connector's Pi support today.

Bonus find outside the six extensions `docs/harnesses.md` catalogues:
**`senad-d/ObservMe`** emits genuine OTLP logs (a dedicated "Logs" section
with `event.name` values like `session.started`, `llm.request.completed`)
alongside traces and metrics — with `pi-otel`'s opt-in logs and metrics, it
is one of two surveyed extensions covering all three signals. Not yet
researched for token usage or repo identity; flagged here for anyone
extending Pi coverage.

## Strands Agents SDK

**No OTel logs.** The
[official Logs docs](https://strandsagents.com/docs/user-guide/observability-evaluation/logs/)
state plainly that "Strands SDK uses Python's standard `logging` module"
(the TypeScript SDK likewise uses a plain console/Pino-compatible `Logger`
interface) — no `LoggerProvider`, no OTel Logs API, no OTLP log exporter.
`StrandsTelemetry` (`strands/telemetry/config.py` in `strands-agents/sdk-python`)
exposes only `setup_console_exporter()`/`setup_otlp_exporter()` (spans) and
`setup_meter()` (metrics); there is no `setup_logs_exporter()`. Prompt and
completion content rides span events (`gen_ai.user.message`,
`gen_ai.assistant.message`, `gen_ai.tool.message`) or an opt-in span
attribute mode, never a standalone logs signal. Traces and metrics are
native and already catalogued in
[docs/harnesses/genai-scopes.md](harnesses/genai-scopes.md) and
[docs/metrics.md](metrics.md).

## Cline and Hermes (unchanged, restated for completeness)

- **Cline**: metrics are native but degraded on the `main` branch — the
  classic turns/tokens/cost/cache/tools/api/workspace instruments have no
  recording call site there and now flow as OTel log events instead; the
  full instrument set survives only on the `legacy-extension` branch. See
  [docs/metrics.md](metrics.md#cline).
- **Hermes**: all three signals are native, but scoped entirely to gateway
  health/diagnostics (`hermes.gateway_health`, `hermes.gateway_diagnostic`,
  `hermes.cron_execution`, gauges, and their logs) — none carry agent,
  session, tool, or token data. See [docs/harnesses.md](harnesses.md#hermes).

## Sources

- Claude Code monitoring: https://code.claude.com/docs/en/monitoring-usage
- Claude Code Agent SDK observability: https://code.claude.com/docs/en/agent-sdk/observability
- GitHub Copilot CLI OpenTelemetry: https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring
- VS Code Copilot Chat monitoring: https://code.visualstudio.com/docs/agents/guides/monitoring-agents
- GitHub Copilot usage/activity APIs (non-OTel): https://docs.github.com/en/copilot/reference/metrics-data
- Claude Code monitoring metrics (eight counters, checked 2026-10-19):
  https://code.claude.com/docs/en/monitoring-usage
- Pi extension `@amaster.ai/pi-telemetry`: https://www.npmjs.com/package/@amaster.ai/pi-telemetry
- Pi extension `pi-otel-telemetry`: https://github.com/mprokopov/pi-otel-telemetry
  (repo `main` common attrs at
  [0beaa10](https://github.com/mprokopov/pi-otel-telemetry/blob/0beaa10e5bf82865260fccd3b4bc95c117d8238f/index.ts);
  npm 1.0.0 carries `tool.name` labels only — checked 2026-10-19)
- Pi extension `pi-otel` (NikiforovAll) 0.3.0 — traces, opt-in logs
  (`PI_OTEL_LOGS=1`) and metrics (`PI_OTEL_METRICS=1`):
  https://www.npmjs.com/package/pi-otel/v/0.3.0; logs config:
  https://nikiforovall.blog/pi-otel/configuration
- Pi extension `maxmalkin/pi-OTEL`:
  https://github.com/maxmalkin/pi-OTEL/blob/9c3c26718a005f5d658fd06f6a7fcb3e396227b4/src/index.ts
- Pi extension `@the-agency/pi-observability` (traces only):
  https://www.npmjs.com/package/@the-agency/pi-observability/v/0.4.0
- Pi extension `devkade/pi-opentelemetry`: source `src/trace/provider.ts`,
  `src/metrics/collector.ts`, `src/metrics/provider.ts` —
  https://github.com/devkade/pi-opentelemetry/tree/ac223fd08c58fe06d903c62d5c5318e1a7f9d42a/src
- Pi vendor telemetry package `@earendil-works/pi-telemetry` (span contract,
  no exporter): https://github.com/earendil-works/pi/blob/v1.3.0/packages/telemetry/README.md
- Pi extension `senad-d/ObservMe` (all three signals):
  https://github.com/senad-d/ObservMe/blob/39c2b1f71faef63b11909f5b0385b97d16cee6e1/package.json
- Codex native opt-in traces and metrics-exporter behavior (0.163.0,
  checked 2026-10-19): https://github.com/openai/codex/releases/tag/rust-v0.163.0
  and [docs/harnesses/codex.md](harnesses/codex.md)
- OpenCode AI SDK span export since v1.4.5 (checked 2026-10-19):
  https://github.com/anomalyco/opencode/releases/tag/v1.4.5 and
  https://github.com/anomalyco/opencode/pull/22526
- Pi findings above (checked 2026-10-19): [docs/harnesses/pi.md](harnesses/pi.md)
- Strands logs docs: https://strandsagents.com/docs/user-guide/observability-evaluation/logs/
- Strands SDK source: https://github.com/strands-agents/sdk-python (`strands/telemetry/config.py`, `tracer.py`)
