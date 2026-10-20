# Coding-agent harness telemetry reference

This document records what each coding-agent harness exports over OpenTelemetry,
scoped to the two concerns that matter for the connector's canonical edge:
**token usage** and **project/workspace/repo identity**. It does not propose
connector support; it records what the sources say so a decision can rest on
evidence, and the summary marks which harnesses the connector sorts today.

Research refreshed 2026-08-20 against primary sources (official docs and the
harnesses' own source trees). Provider schemas are not stable APIs; treat the
attribute names below as snapshots, not contracts.

Separate documents track what each supported harness emits after
normalization: [docs/canonical-attributes.md](canonical-attributes.md) holds
the closed canonical vocabulary, and [docs/harnesses/](harnesses/) holds one
raw → canonical mapping matrix per harness. On 2026-10-19 each supported
harness was re-audited there with pinned citations; where this file's summary
predates those audits, the matrix files are authoritative. The summary below
describes the wire, not canonical output: a "yes" in the token-usage column
means the harness can put usage on the wire, not that every emitted span
carries it.
Conditional availability lives in each harness's matrix file. For the
narrower question of which OTel signal types (traces, logs, metrics) each
harness natively supports, see [docs/otel-signals.md](otel-signals.md) — it
answers that question directly and corrects the Signal column below for
Claude Code, whose native logs signal falls outside this file's scope.

## Summary

| Harness | Signal | Token usage in OTel | Project/repo identity in OTel | OTel metrics | Connector today |
| --- | --- | --- | --- | --- | --- |
| **Claude Code** | native traces (beta) + native logs (see [docs/otel-signals.md](otel-signals.md)) | yes (`gen_ai.usage.*`) | no repo identity; not the focus here | yes (native) | yes (traces edge) |
| **Codex** | structured logs; native traces opt-in (`otel.trace_exporter`, 0.163.0) | provider-dependent (`response.completed` relays what the provider returns) | conversation ID, no repo path | yes (native) | yes (logs edge) |
| **Cline** | metrics + logs (no traces) | yes (log events) | partial, **hashed** | yes (native) | no |
| **Pi** | traces (via extensions) | yes | **yes, real `cwd` path** | via extensions | yes (traces edge, `@amaster.ai/pi-telemetry`) |
| **Kilo** | traces + logs | thin (removed from AI SDK spans) | no (SQLite only) | no | no |
| **Cursor** | metrics + logs (native, Enterprise beta) | yes (native) | no (hooks only) | yes (native) | yes (logs edge) |
| **Hermes** | traces + metrics + logs (gateway health only) | no (plugin only) | no | yes, health gauges only | no |
| **OpenCode** | traces + logs (native, new) | yes (native spans, no cost) | no (plugins add it) | via plugins | yes (traces edge) |
| **OpenHands** | traces (native) | yes (LLM spans; streamed completions carry none) | no (pass it yourself) | no | yes (traces edge) |

Codex, Cursor, Claude Code, OpenCode, Pi, OpenHands, and the GenAI-semconv
scopes are the connector's supported edges; the root [README](../README.md)
lists the
required configuration for each. This file focuses on Cline and Kilo,
with Pi, Cursor, Hermes, OpenCode, and OpenHands added from the same research.
The `## OTel metrics` section below covers the metrics dimension.

## Cline

Native, opt-in OpenTelemetry. **Metrics and logs only — no distributed traces**
(the docs list `Distributed tracing (not yet implemented)`).

- Configure via `CLINE_OTEL_*` env vars or the enterprise Remote Configuration
  dashboard. Env overrides dashboard. Set the vars in the shell that launches
  the editor (GUI launches do not inherit them).
- Variables: `CLINE_OTEL_TELEMETRY_ENABLED`, `CLINE_OTEL_METRICS_EXPORTER` /
  `CLINE_OTEL_LOGS_EXPORTER` (`otlp`/`console`), `CLINE_OTEL_EXPORTER_OTLP_PROTOCOL`
  (`grpc`/`http/protobuf`/`http/json`), `CLINE_OTEL_EXPORTER_OTLP_ENDPOINT`,
  `CLINE_OTEL_EXPORTER_OTLP_HEADERS`, plus per-signal endpoints and batch tuning.
- `service.name = cline` by default; settable via `OTEL_SERVICE_NAME` /
  `OTEL_RESOURCE_ATTRIBUTES`.
- Metrics: 30+ instruments under `cline.*` (`cline.turns.total`,
  `cline.tokens.input/output.total`, `cline.cost.total`, `cline.cache.*`,
  `cline.tool.calls.total`, `cline.errors.*`, `cline.hooks.*`,
  `cline.ai_output.accepted/rejected.*`).
- Logs: structured events namespaced `user.*`, `task.*`, `workspace.*`, `ui.*`,
  `hooks.*`, `host.*`, `cline.test.*`.

**Token usage — yes.** On log events:
- `task.tokens`: `tokens_in`, `tokens_out`, `cached_tokens`, `cost`, `model`,
  `provider`, `task_id`.
- `task.conversation_turn`: `tokens_in`/`tokens_out` plus `role`, `provider`,
  `model`, `source`, `mode`.
- `task.completed`: `tokens_total`, `model`, `provider`, `task_id`, `duration_ms`.

**Project/repo identity — partial and anonymized.** `workspace.*` events exist
(`workspace.initialized` with `root_count`, `vcs_types`, `has_git`,
`has_mercurial`; `workspace.vcs_detected` with `vcs_type`, `root_path_hash`;
`workspace.path_resolved`), but file paths and branch names are **hashed** to
preserve privacy. You get VCS type and a hashed root path, not the repo name,
directory path, or remote URL. To get a usable repo dimension you must inject it
yourself via `OTEL_RESOURCE_ATTRIBUTES` (e.g. `project=<name>`), which then
rides every log record and metric.

## Pi

No single built-in OTLP surface; telemetry comes from **third-party
extensions**, which differ in attribute naming. The relevant ones:

- `pi-otel-telemetry` (auto-discovered by pi from the extensions directory;
  [upstream README](https://github.com/mprokopov/pi-otel-telemetry), checked
  2026-10-19).
- `@the-agency/pi-observability` (GenAI-semconv spans).
- `pi-otel` (NikiforovAll) and `maxmalkin/pi-OTEL` (GenAI-semconv spans).
- `devkade/pi-opentelemetry` (traces + metrics + diagnostics, privacy profiles).

Config is via `~/.pi/agent/settings.json` (or project `.pi/settings.json`)
`otel` block, or `OTEL_*`/`PI_OTEL_*` env vars.

**Token usage — yes.**
- `pi-otel-telemetry`: `session.tokens.input`/`output` (session span),
  `llm.usage.input_tokens`/`output_tokens` (turn span), plus
  `pi.tokens.input`/`pi.tokens.output` counters.
- `@the-agency/pi-observability`: `gen_ai.usage.input_tokens`,
  `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`,
  `total_tokens`, plus `cost.input_usd`/`output_usd`/`total_usd`.

**Project/repo identity — yes, real path, un-hashed.**
- `pi-otel-telemetry`: `session.id` (session file path), `session.cwd`
  (**working directory**), `user.email`, `user.name`, `user.full_name`,
  `host.name`.
- `@the-agency/pi-observability`: `pi.session.id`, `pi.session.cwd`
  (**working directory**), `pi.session.file` (path to `.pi` session file), plus
  loaded skills/tools/commands.
- Upstream pi session transcripts carry `cwd` in the session header; the
  workspace label derives from the cwd basename.

Pi is the only harness of the three that exports the actual working directory
(un-hashed) alongside token usage. Caveat: attribute names are not standardized
across extensions (custom `session.cwd`/`llm.usage.*` vs `gen_ai.*` +
`pi.session.*`).

## Kilo

Kilo Code CLI (`@kilocode/cli`, a fork of OpenCode). Native OTLP export.

- Set `experimental.openTelemetry` (default on) or
  `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_EXPORTER_OTLP_HEADERS`/
  `OTEL_RESOURCE_ATTRIBUTES` → exports traces and logs to OTLP/HTTP.
- Request spans carry `http.method`, `http.path`, route params like
  `session.id` and `message.id`, and internal params under the `opencode.*`
  namespace.

**Token usage — thin in OTLP.** The AI SDK used to emit `gen_ai.*`/`ai.*`
spans (input/output/total tokens, mapped in the PostHog exporter), but
**PR #9669 removed `gen_ai.*`/`ai.*` span emission entirely** for privacy.
Kilo still tracks token usage internally (`getUsage`: input/output/reasoning
and cache tokens, plus cost), and the PostHog analytics path captures
`trackLlmCompletion` (`inputTokens`/`outputTokens`/`cacheReadTokens`/
`cacheWriteTokens`/`cost`) and `trackSessionEnd` — but the OTLP-exported span
story for tokens is now thin.

**Project/repo identity — not in OTLP.** The session model internally holds
`projectID`, `workspaceID`, and `directory` (in the SQLite session store), but
these are not exported as span attributes on the OTLP path — only `session.id`
and `message.id` route params surface. To get a repo dimension you must inject
it via `OTEL_RESOURCE_ATTRIBUTES`.

## Cursor

Cursor (Anysphere) now has **native OpenTelemetry Export**, but it's an
**Enterprise-plan beta**, server-side, and **metrics + logs only — no traces**.
This updates the earlier assumption that Cursor's OTel only came from
hooks/plugins.

- Configured by admins in **Team Settings → OpenTelemetry Export** (server-side,
  not a client env-var toggle). OTLP/HTTP binary protobuf to `/v1/metrics` and
  `/v1/logs`; gRPC and JSON not supported. Scope `cursor.telemetry`/`0.1.0`.
- Covers the desktop, cli, cloud_agent, bugbot, and grok_bot surfaces via
  `cursor.surface`. No separate native CLI OTel story exists.
- Normal delivery: metrics at-most-once, logs at-least-once (dedupe on
  `cursor.event.id`). No prompt content.

**Token usage — yes, natively.** Metric `cursor.token.usage` (by
`cursor.token.type` = input/output/cache_read/cache_creation) plus the
`cursor.api.request` log carrying `input_tokens`/`output_tokens`/
`cache_read_tokens`/`cache_creation_tokens`, and best-effort `cursor.cost.usage`.
Metric datapoints carry **no correlation IDs**; per-session token attribution
requires joining the `cursor.api.request` logs on `cursor.conversation.id`.

**Project/repo identity — not native.** The native wire exposes only opaque
team/user ids, surface, entrypoint, and a conversation UUID — no working
directory, repo name, branch, or project. You must use hook tooling to get repo
identity:

- **`opentelemetry-hooks`** (`o11y-dev`): installs into `~/.cursor/hooks.json`,
  emits GenAI-semconv **traces** + logs with `gen_ai.client.workspace`,
  `gen_ai.client.repository_root`, `vcs.repository.name`, `vcs.ref.head.name`,
  and a SHA-256 of a credential-free normalized Git remote
  (`gen_ai.client.repository.remote.sha256`). Canonical ops: `chat`/
  `execute_tool`/`invoke_agent`.
- **`last9/cursorscope`**: Node hook collector; span shape `invoke_agent Cursor`
  (attrs `cursor.repo`, `gen_ai.request.model`, `gen_ai.conversation.id`),
  `execute_tool` (with `gen_ai.tool.name`), subagent `invoke_agent`.
- **OpenLIT CLI**: supports Cursor via vendor hooks, not native.

## Hermes

Hermes Agent is **Nous Research's open-source autonomous CLI + multi-channel
messaging-gateway agent** (`github.com/NousResearch/hermes-agent`, Python),
distinct from the Hermes 3/4 model families. It tops OpenRouter token-volume
rankings (~17T+ tokens).

Hermes **does** have native in-tree OpenTelemetry, but it's **scoped to gateway
service-health/diagnostics only** and deliberately excludes token usage, LLM
calls, agent/session/tool spans, and repo/workspace identity.

**In-tree OTel** (verified from `agent/monitoring/` source): emits
`hermes.gateway_health`, `hermes.gateway_diagnostic`, `hermes.cron_execution`
spans plus `hermes.*` gauges and logs. OTLP/HTTP only, opt-in via
`pip install 'hermes-agent[otlp]'`, configured under `monitoring.export.otlp` in
`config.yaml`. Source is explicit: *"no prompts, messages, tool args/results,
session history, or usage analytics."* Redaction strips PII and paths.

**Token usage — not in-tree.** Token data lives on Hermes's lifecycle hooks
(`post_api_request` carries a `usage` dict), consumed only by plugins. The
de-facto plugin **`briancaffey/hermes-otel`** emits `agent → skill → llm → api →
tool` span trees with tokens under dual conventions:
`gen_ai.usage.input_tokens`/`output_tokens`/`cache_read`/`cache_creation`/
`reasoning` **and** `llm.token_count.*`. Cost is a separate plugin
(`nujovich/hermes-telemetry`, SQLite, not OTel-native).

**Project/repo identity — absent.** Neither in-tree OTel nor the main plugin
emits a repo, working directory, or project. Closest are operator-set
`project_name`/resource tags, gateway sender `user.id`, and tool-touched file
paths. Maintainer policy (PR #9596 closed as not-planned) keeps observability
backends as standalone plugins, not core.

## OpenCode

OpenCode (`anomalyco/opencode`, formerly `sst/opencode`) is the most-starred
open-source coding agent (~172K stars, ~7.5M MAU per opencode.ai). Native
OTel (traces + logs only) is undocumented — the AI SDK span export shipped
in v1.4.5 (2026-04-15,
[anomalyco/opencode#22526](https://github.com/anomalyco/opencode/pull/22526));
releases since then emit the Vercel AI SDK spans the connector claims when
`experimental.openTelemetry: true` and `OTEL_EXPORTER_OTLP_ENDPOINT` is set
(re-verified against 1.19.3, checked 2026-10-19; see
[docs/harnesses/opencode.md](harnesses/opencode.md)). The richer story
(metrics, cost, repo identity) comes from plugins.

Two independent native paths:
- **Vercel AI SDK spans** (`ai.streamText`, `ai.toolCall`), enabled by
  `experimental.openTelemetry: true`; every span gets `session.id` (`ses_…`)
  via a tracer proxy.
- **Effect spans** (`Tool.execute` with `tool.name`/`message.id`/`tool.call_id`)
  and Effect logs, exported whenever `OTEL_EXPORTER_OTLP_ENDPOINT` points at a
  collector.

**Token usage — yes, native on spans.** `ai.usage.*` (input/output/
cache-read/reasoning/total) on the root span, plus
`gen_ai.usage.input_tokens`/`output_tokens` on per-step spans. No cost. Richer
per-message token+cost data exists internally (`Session.getUsage`) but is not
exported to OTel natively.

**Project/repo identity — none natively.** Only `session.id`, a user id (the
literal `unknown` unless the config sets `username`), and a random
`opencode.run` id. No cwd, repo, branch, or remote URL. Plugins add
identity:
- **`felixti/opencode-otel-plugin`** — OTel GenAI semconv matching the
  canonical model exactly (`invoke_agent opencode` → `chat {model}` →
  `execute_tool {tool}`) with full VCS identity (`vcs.repository.url.full`,
  `vcs.repository.ref.name`, `enduser.id` = git author email,
  `opencode.directory`, `opencode.worktree`). No cost.
- **`@devtheops/opencode-plugin-otel`** — de-facto standard (SigNoz/Grafana/
  Honeycomb/Datadog docs). Metrics + logs + traces using **OpenInference**
  conventions; full token/cost metrics; repo identity only as a hashed
  `project.id` (`Hash.fast("git-remote:<normalized-remote>")`).
- **`opentelemetry-hooks`** — traces + logs with `cwd` + `session_id`; no tokens.

## OpenHands

OpenHands (`OpenHands/OpenHands`, formerly All-Hands-AI) runs on the
`software-agent-sdk` (Python), which is where native OTel lives — via the
Laminar SDK (`lmnr`) as the OTel layer, exporting standard OTLP traces. The
main repo itself is now Agent Canvas, a TypeScript web app with an optional
Electron desktop build and PostHog analytics, not OTel.

**Token usage — yes, on LLM spans.** `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, `llm.usage.total_tokens`, `gen_ai.usage.cost`,
`gen_ai.system`, `gen_ai.request.model`, `gen_ai.response.model`. Each LLM
call produces two chat spans — the SDK's `llm.<model>` wrapper and lmnr's
nested `litellm.completion` — and the wrapper carries the cache-token counts
(`gen_ai.usage.cache_read_input_tokens`/`cache_creation_input_tokens`, `0`
when nothing hit the cache); both spans carry the same values, so summing
cache tokens across all chat spans counts each call twice. Reasoning tokens
stay in-process only. Wire inspection on 2026-10-19 (SDK 1.56.0, lmnr 0.7.67)
found streamed completions carrying no token usage upstream at all; the
per-request dollar cost (`gen_ai.usage.cost`) rides the wrapper spans (see
[docs/harnesses/openhands.md](harnesses/openhands.md)).

**Project/repo identity — not attached.** The workspace working dir exists
in-process but is never written to spans; the desktop app deliberately does not
forward repo/branch/workspace to the agent-server. You get
`lmnr.association.properties.session_id` (conversation UUID), `user_id` (if set
by the deployment), and `conversation.tags.*` (automation run identity, not
repo). To get repo identity you must pass it yourself via `observability_metadata`
or `tags` at conversation start.

**Desktop UI environment.** In the local Canvas stack (the `agent-canvas`
command and the Electron app, which imports the same launcher in-process),
the agent-server child starts with Canvas's own process environment merged
in, so OTLP variables set where Canvas starts reach the SDK (checked
2026-10-19; see [docs/harnesses/openhands.md](harnesses/openhands.md)). An
app launched from a desktop launcher sees the desktop session's environment,
not a shell's.

## OTel metrics

Whether each harness emits OTel metrics, with the instrument names. Research
refreshed 2026-08-21; instrument names come from source or vendor docs unless
noted.

**Native OTel metrics:**
- **Codex** — in-tree `codex-rs/otel` crate. Counters `codex.thread.started`,
  `codex.conversation.turn.count`, `codex.tool.call`, `codex.tool.unified_exec`,
  `codex.websocket.request`, `codex.plugins.startup_sync`; histograms
  `codex.turn.e2e_duration_ms`, `codex.turn.ttft/ttfm.duration_ms`,
  `codex.turn.token_usage` (by `token_type`), `codex.turn.tool.call`,
  `codex.mcp.tools.*.duration_ms`, `codex.websocket.request.duration_ms`. The
  default `otel.metrics_exporter` is `statsig`, which sends to OpenAI and
  follows `[analytics] enabled`; since Codex 0.163.0
  ([openai/codex#52278](https://github.com/openai/codex/issues/52278)) a
  custom OTLP metrics exporter works even with analytics turned off — set
  `[otel.metrics_exporter.otlp-http]` in `~/.codex/config.toml`, or
  `otel.metrics_exporter = "none"` to disable (checked 2026-10-19; see
  [docs/harnesses/codex.md](harnesses/codex.md)).
- **Claude Code** — eight counters under meter scope
  `com.anthropic.claude_code`: `claude_code.session.count`,
  `claude_code.lines_of_code.count`, `claude_code.pull_request.count`,
  `claude_code.commit.count`, `claude_code.cost.usage`,
  `claude_code.token.usage` (by `type`),
  `claude_code.code_edit_tool.decision`, `claude_code.active_time.total`
  (delta temporality by default).
  `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `OTEL_METRICS_EXPORTER=otlp`
  (checked 2026-10-19; see
  [docs/harnesses/claude-code.md](harnesses/claude-code.md)).
- **openai-v2 / util-genai** — histograms `gen_ai.client.operation.duration`,
  `gen_ai.client.token.usage`; util-genai additionally defines
  `gen_ai.client.operation.time_to_first_chunk` and
  `gen_ai.client.operation.time_per_output_chunk`, recorded only by
  instrumentations that use its shared stream wrappers (openai-v2 2.4b0
  never records them). util-genai ≥ 1.0b0 ignores
  `OTEL_SEMCONV_STABILITY_OPT_IN` (checked 2026-10-19; see
  [docs/harnesses/genai-scopes.md](harnesses/genai-scopes.md)). Standard
  OTel SDK config.
- **Strands** — in-tree `MetricsClient`. Counters `strands.event_loop.cycle_count`,
  `strands.event_loop.start_cycle`/`end_cycle`, `strands.tool.call_count`/
  `success_count`/`error_count`; histograms `strands.event_loop.latency`,
  `strands.tool.duration`, `strands.event_loop.{input,output,cache_read,
  cache_write}.tokens`, `strands.model.time_to_first_token`.
- **Cline** — 30+ `cline.*` instruments via remote config (Enterprise):
  `cline.turns.*`, `cline.tokens.input/output.*`, `cline.cost.*`,
  `cline.cache.*`, `cline.tool.calls.*`, `cline.errors.*`,
  `cline.api.ttft.seconds`/`duration.seconds`/`throughput.tokens_per_second`,
  `cline.hooks.*`, `cline.ai_output.accepted/rejected.*`,
  `cline.grpc.response.size_bytes`, gauge `cline.workspace.active_roots`.
- **Cursor** — Enterprise beta, server-side: `cursor.token.usage` (by
  `cursor.token.type`), `cursor.tool.calls` (by `cursor.tool.kind`),
  `cursor.cost.usage`. Delta temporality. Metrics + logs only, no traces.
- **Hermes** — gauges only, gateway/cron health: `hermes.gateway.up/state/busy/
  drainable/active_agents/background_work/background_delegations/
  restart_requested`, `hermes.platform.up/degraded`, cron scheduler gauges. No
  token/cost/usage metrics.

**No native OTel metrics:**
- **Kilo** — native OTLP traces + logs only; internal telemetry goes to PostHog.
- **OpenHands** — OTel traces only (via Laminar); token/cost/latency metrics
  exist only in-process (`llm.metrics`, `conversation.conversation_stats`), not
  as an OTel/Prometheus endpoint.

**Metrics via third-party extensions/plugins:**
- **Pi** — no native telemetry; every instrument comes from an extension
  (checked 2026-10-19; see [docs/harnesses/pi.md](harnesses/pi.md)).
  `pi-otel-telemetry` (mprokopov): 8 instruments — counters
  `pi.tokens.input`/`output`, `pi.tool.calls`, `pi.tool.errors`, `pi.turns`,
  `pi.prompts`; histograms `pi.tool.duration` (ms), `pi.session.duration` (s)
  (npm 1.0.0 carries only `tool.name` labels; the common
  `user.name`/`host.name`/`environment` attrs exist only on repo `main`).
  `devkade/pi-opentelemetry`: counters `pi.session.count`, `pi.turn.count`,
  `pi.tool_call.count`, `pi.tool_result.count`, `pi.prompt.count`,
  `pi.token.usage`, `pi.cost.usage`; histograms `pi.session.duration`,
  `pi.turn.duration`, `pi.tool.duration`. `pi-otel` (NikiforovAll): opt-in
  `PI_OTEL_METRICS=1` — histograms `gen_ai.client.operation.duration`,
  `gen_ai.client.token.usage`, `gen_ai.client.tool_calls_per_operation` and
  counter `gen_ai.client.tool.calls`. `maxmalkin/pi-OTEL`: counters
  `gen_ai.client.token.usage`, `gen_ai.client.cost`, `pi.tool.calls`,
  `pi.session.compactions`, `pi.provider.retries`, `pi.turn.cancellations`;
  histograms `gen_ai.client.operation.duration`, `pi.tool.duration`.
  `@the-agency/pi-observability`: no metrics.
- **OpenCode** — none native (OpenCode registers only OTLP trace and log
  exporters). Metrics come from plugins:
  `@devtheops/opencode-plugin-otel` v2.0.1 (`opencode.session.count`,
  `opencode.token.usage`, `opencode.cost.usage`, `opencode.commit.count`,
  `opencode.tool.duration`, `opencode.cache.count`,
  `opencode.session.duration`, `opencode.message.count`,
  `opencode.session.token.total`, `opencode.session.cost.total`,
  `opencode.model.usage`, `opencode.retry.count`, `opencode.subtask.count`;
  v2 no longer emits `opencode.lines_of_code.count`/`.total`);
  `@gcornut/opencode-otel` offers 8 counters with a
  `telemetryProfile: "claude-code"` rename (checked 2026-10-19; see
  [docs/harnesses/opencode.md](harnesses/opencode.md)).

## Relevance to the connector

The native OpenCode path is now handled: the connector's traces edge claims
groups whose instrumentation scope is exactly `opencode` and normalizes the
Vercel AI SDK spans. Plugin surfaces (`felixti/opencode-otel-plugin` GenAI
semconv, `@devtheops/opencode-plugin-otel` OpenInference) remain future work.

GitHub Copilot native traces follow the same path: the GenAI edge claims
instrumentation scope `github.copilot` (CLI GA since v1.0.4; VS Code Chat
speaks the identical vocabulary). The cloud coding agent has no OTLP export,
so its hooks path stays out of scope; the JetBrains extension shows no
telemetry at all. Research record: verified against primary sources on
2026-08-22.

Pi has support too, through a different extension than this section's
2026-08-20 research surveyed: the traces edge claims groups scoped
`@amaster.ai/pi-telemetry` (the only maintained open OTLP exporter found in the
npm ecosystem on 2026-08-22) and normalizes its custom vocabulary. The other
extensions below (`pi-otel-telemetry`, `@the-agency/pi-observability`,
`devkade/pi-opentelemetry`, …) emit different span names and scopes; each would
need its own claim profile if it gains adoption worth tracking. The repo-level
repo/cwd identity these extensions carry is not yet mapped into canonical
output — the connector drops nothing from the raw pipeline, so identity stays
available there.

OpenHands has support too: the traces edge claims `lmnr.tracer` scope groups
carrying OpenHands markers (the SDK's conversation/step span names or its
delegate flag) and normalizes them into the canonical vocabulary. The
2026-10-19 wire inspection (SDK 1.56.0, lmnr 0.7.67) found streamed
completions carrying no token usage upstream, with the SDK's per-request
dollar cost riding the `llm.<model>` wrapper spans. The desktop UI merges
its own process environment into the agent-server, so OTLP variables set
where Canvas starts reach the SDK; an app launched from a desktop launcher
sees the desktop session's environment, not a shell's.

The connector does not sort Cline, Kilo, or Hermes today, and
the approved GenAI semconv design (which claims by instrumentation scope:
`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
`strands.telemetry`) does not cover them either.

- **Cline** emits metrics/logs, not traces, so it does not fit a traces-edge
  normalizer.
- **Kilo** emits `opencode.*`-namespaced spans and deliberately strips
  `gen_ai.*`; no match.
- **Cursor**: the native metrics/logs surface already feeds the connector's
  logs edge (re-verified against the 2026-08-22 wire reference: all ten log
  logs edge covers all ten log events and three metrics; `plugin.installed`
  carries no conversation id, so the standing no-conversation-id policy
  declines it).
  Correction records annotate the joined chat span with the correction kind
  rather than dropping its token totals — downstream decides billing
  semantics. Traces and repo identity come only from hook tooling
  (opentelemetry-hooks/cursorscope), whose `gen_ai.*` scopes would need the
  allowlist extension.
- **Hermes** exposes neither token usage nor repo identity in-tree; both require
  plugins, so it's the weakest fit of the group.

The third-party `o11y-dev/opentelemetry-hooks` project already normalizes
Antigravity, Claude Code, Codex, Cursor, Gemini CLI, Copilot, OpenCode, and
Windsurf into `gen_ai.*` spans. Worth studying before building more
normalizers; it may be a reference implementation or a reuse/collaboration
candidate.

## Sources

- Cline OpenTelemetry: https://docs.cline.bot/enterprise-solutions/monitoring/opentelemetry
- Cline OTel events reference: https://docs.cline.bot/enterprise-solutions/monitoring/opentelemetry-events
- Cline source (telemetry): https://github.com/cline/cline/blob/main/src/services/telemetry
- Pi extensions: `pi-otel-telemetry`, `@the-agency/pi-observability`,
  `pi-otel` (NikiforovAll), `maxmalkin/pi-OTEL`, `devkade/pi-opentelemetry`
  (pi.dev / npm); supported exporter:
  https://www.npmjs.com/package/@amaster.ai/pi-telemetry
- Pi session transcript format (cwd header): https://openusage.sh/docs/providers/pi/
- Kilo Code CLI: https://kilo.ai/docs/code-with-ai/platforms/cli
- Kilo telemetry source: https://github.com/Kilo-Org/kilocode (packages/kilo-telemetry)
- Kilo PR #9669 (removes `ai.*`/`gen_ai.*` span emission):
  https://github.com/Kilo-Org/kilocode/pull/9669
- Cursor OpenTelemetry Export: https://cursor.com/docs/enterprise/opentelemetry-export
- Cursor OTel Wire Reference: https://cursor.com/docs/enterprise/opentelemetry-export/wire
- Cursor hook tooling: https://github.com/o11y-dev/opentelemetry-hooks,
  https://github.com/last9/cursorscope, https://github.com/openlit/openlit
- Hermes Agent: https://github.com/NousResearch/hermes-agent (agent/monitoring/)
- Hermes OTel plugin (tokens): https://github.com/briancaffey/hermes-otel
- Hermes rejected in-tree OTel PR (policy): https://github.com/NousResearch/hermes-agent/pull/9596
- Cross-harness OTel normalization: https://github.com/o11y-dev/opentelemetry-hooks
- OpenCode: https://github.com/anomalyco/opencode (formerly `sst/opencode`);
  plugin https://github.com/DEVtheOPS/opencode-plugin-otel (v2.0.1, checked
  2026-10-19)
- OpenHands SDK observability: https://docs.openhands.dev/sdk/guides/observability,
  https://docs.openhands.dev/sdk/guides/metrics
- Codex OTel metrics: https://github.com/openai/codex/blob/main/codex-rs/otel/README.md
- Claude Code monitoring (metrics): https://code.claude.com/docs/en/monitoring-usage
- Strands metrics: https://strandsagents.com/docs/user-guide/observability-evaluation/metrics/
