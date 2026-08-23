# Coding-agent harness telemetry reference

This document records what each coding-agent harness exports over OpenTelemetry,
scoped to the two concerns that matter for the connector's canonical edge:
**token usage** and **project/workspace/repo identity**. It does not propose
connector support; it records what the sources say so a decision can rest on
evidence.

Research refreshed 2026-08-20 against primary sources (official docs and the
harnesses' own source trees). Provider schemas are not stable APIs; treat the
attribute names below as snapshots, not contracts.

## Summary

| Harness | Signal | Token usage in OTel | Project/repo identity in OTel | OTel metrics |
| --- | --- | --- | --- | --- |
| **Claude Code** | native traces (beta) | yes (`gen_ai.usage.*`) | no repo identity; not the focus here | yes (native) |
| **Codex** | structured logs | yes (`response.completed`) | conversation ID, no repo path | yes (native) |
| **Cline** | metrics + logs (no traces) | yes (log events) | partial, **hashed** | yes (native) |
| **Pi** | traces (via extensions) | yes | **yes, real `cwd` path** | via extensions |
| **Kilo** | traces + logs | thin (removed from AI SDK spans) | no (SQLite only) | no |
| **Cursor** | metrics + logs (native, Enterprise beta) | yes (native) | no (hooks only) | yes (native) |
| **Hermes** | traces + metrics + logs (gateway health only) | no (plugin only) | no | yes, health gauges only |
| **OpenCode** | traces + logs (native, new) | yes (native spans, no cost) | no (plugins add it) | via plugins |
| **OpenHands** | traces (native) | yes (LLM spans) | no (pass it yourself) | no |

Claude Code and Codex are the connector's existing edges and `docs/design.md`
covers them. This file focuses on Cline, Pi, and Kilo, with Cursor, Hermes,
OpenCode, and OpenHands added from the same research. The `## OTel metrics`
section below covers the metrics dimension.

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

- `pi-otel-telemetry` (auto-installed at `~/.pi/agent/extensions/otel-telemetry/`).
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
- Covers both IDE and CLI surfaces (`cursor.surface` = desktop/cli/cloud_agent/
  bugbot). No separate native CLI OTel story exists.
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

OpenCode (`sst/opencode`, Anomaly/SST) is the most-starred open-source coding
agent (~172K stars, ~7.5M MAU per opencode.ai). Native OTel exists in-tree on
the dev branch (traces + logs only), but it's new and undocumented — on
released versions (`opencode-ai` 1.1.51–1.1.65) the `experimental.openTelemetry`
flag alone produced no spans. The richer story (metrics, cost, repo identity)
comes from plugins.

Two independent native paths:
- **Vercel AI SDK spans** (`ai.streamText`, `ai.toolCall`), enabled by
  `experimental.openTelemetry: true`; every span gets `session.id` (`ses_…`)
  via a tracer proxy.
- **Effect spans** (`Tool.execute` with `tool.name`/`message.id`/`tool.call_id`)
  and Effect logs, exported whenever `OTEL_EXPORTER_OTLP_ENDPOINT` points at a
  collector.

**Token usage — yes, native on spans.** `ai.usage.*` (input/output/
cache-read/cache-write/reasoning/total) on the root span, plus
`gen_ai.usage.input_tokens`/`output_tokens` on per-step spans. No cost. Richer
per-message token+cost data exists internally (`Session.getUsage`) but is not
exported to OTel natively.

**Project/repo identity — none natively.** Only `session.id`, OS username, and
a random `opencode.run` id. No cwd, repo, branch, or remote URL. Plugins add
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
main repo itself is now the Electron/TypeScript desktop app with PostHog
analytics, not OTel.

**Token usage — yes, on LLM spans.** `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, `llm.usage.total_tokens`,
`gen_ai.usage.cost`/`input_cost`/`output_cost`, `gen_ai.system`,
`gen_ai.request.model`, `gen_ai.response.model`. No cache-token attributes on
spans by default; fine-grained cache/reasoning tokens exist only in-process
(served over the agent-server API), not as OTel.

**Project/repo identity — not attached.** The workspace working dir exists
in-process but is never written to spans; the desktop app deliberately does not
forward repo/branch/workspace to the agent-server. You get
`lmnr.association.properties.session_id` (conversation UUID), `user_id` (if set
by the deployment), and `conversation.tags.*` (automation run identity, not
repo). To get repo identity you must pass it yourself via `observability_metadata`
or `tags` at conversation start.

## OTel metrics

Whether each harness emits OTel metrics, with the instrument names. Research
refreshed 2026-08-21; instrument names come from source or vendor docs unless
noted.

**Native OTel metrics:**
- **Codex** — in-tree `codex-otel` crate. Counters `codex.thread.started`,
  `codex.conversation.turn.count`, `codex.tool.call`, `codex.tool.unified_exec`,
  `codex.websocket.request`, `codex.plugins.startup_sync`; histograms
  `codex.turn.e2e_duration_ms`, `codex.turn.ttft/ttfm.duration_ms`,
  `codex.turn.token_usage` (by `type`), `codex.turn.tool.call`,
  `codex.mcp.tools.*.duration_ms`, `codex.websocket.request.duration_ms`. Needs
  `analytics_enabled = true` plus `[otel.metrics_exporter.otlp-http]` in
  `~/.codex/config.toml`; default exporter is `statsig`.
- **Claude Code** — meter scope `com.anthropic.claude_code`:
  `claude_code.session.count` (Sum/delta), `claude_code.token.usage` (by
  `type`), `claude_code.cost.usage`, `claude_code.lines_of_code.count`.
  `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `OTEL_METRICS_EXPORTER=otlp`.
- **openai-v2 / util-genai** — histograms `gen_ai.client.operation.duration`,
  `gen_ai.client.token.usage`; experimental adds
  `gen_ai.client.operation.time_to_first_chunk`,
  `gen_ai.client.operation.time_per_output_chunk`. Standard OTel SDK config.
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
- **Pi** — no native telemetry. `pi-otel-telemetry`: `pi.tool.duration`
  (histogram), `pi.turns`, `pi.prompts`, `pi.session.duration`.
  `devkade/pi-opentelemetry`: session/turn/tool/prompt/token/cost/duration
  (instrument names not enumerated). `@the-agency/pi-observability` and
  `NikiforovAll/pi-otel` are traces-only (metrics not verified).
- **OpenCode** — native `experimental.openTelemetry` is traces-focused and
  reported broken for `opencode run`. Metrics come from plugins:
  `@devtheops/opencode-plugin-otel` mirrors Claude Code's catalog
  (`opencode.session.count`, `opencode.token.usage`, `opencode.cost.usage`,
  `opencode.lines_of_code.*`, `opencode.tool.duration`, `opencode.cache.count`,
  `opencode.session.duration`, `opencode.message.count`, `opencode.model.usage`,
  `opencode.retry.count`); `@gcornut/opencode-otel` offers 8 counters with a
  `telemetryProfile: "claude-code"` rename.

## Relevance to the connector

The native OpenCode path is now handled: the connector's traces edge claims
groups whose instrumentation scope is exactly `opencode` and normalizes the
Vercel AI SDK spans. Plugin surfaces (`felixti/opencode-otel-plugin` GenAI
semconv, `@devtheops/opencode-plugin-otel` OpenInference) remain future work.

The connector does not sort Cline, Pi, Kilo, Cursor, or Hermes today, and the
approved GenAI semconv design (which claims by instrumentation scope:
`opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
`strands.telemetry`) does not cover them either.

- **Pi** is the closest fit: its extensions emit GenAI-semconv traces with the
  `invoke_agent`/`chat`/`execute_tool` vocabulary, but their span names
  (`pi.session`, `pi.turn`, `gen_ai.chat`) and scopes do not match the planned
  detection rules. Folding Pi in would use the design's "configurable scope
  allowlist" future-work item.
- **Cline** emits metrics/logs, not traces, so it does not fit a traces-edge
  normalizer.
- **Kilo** emits `opencode.*`-namespaced spans and deliberately strips
  `gen_ai.*`; no match.
- **Cursor** natively emits metrics/logs only (no traces) and no repo identity;
  traces and repo identity come only from hook tooling
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
  (pi.dev / npm)
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
- OpenCode: https://github.com/sst/opencode; plugin https://github.com/DEVtheOPS/opencode-plugin-otel
- OpenHands SDK observability: https://docs.openhands.dev/sdk/guides/observability,
  https://docs.openhands.dev/sdk/guides/metrics
- Codex OTel metrics: https://github.com/openai/codex/blob/main/codex-rs/otel/README.md
- Claude Code monitoring (metrics): https://code.claude.com/docs/en/monitoring-usage
- Strands metrics: https://strandsagents.com/docs/user-guide/observability-evaluation/metrics/
