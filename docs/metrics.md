# Coding-agent harness OTel metrics reference

This document records, for each coding-agent harness that emits OpenTelemetry
metrics, the instrument names and the attributes/dimensions each instrument
carries. It complements `docs/harnesses.md`, which covers trace and log
surfaces, and [docs/otel-signals.md](otel-signals.md), which answers
directly whether each harness supports traces, logs, and metrics at all;
this file drills into the metrics dimension only. It does not propose
connector support; it records what the sources say so a decision can rest on
evidence.

Research refreshed 2026-08-21 against primary sources (official docs and the
harnesses' own source trees). Provider schemas are not stable APIs; treat the
attribute names below as snapshots, not contracts.

Each entry carries a sourcing marker:

- **Source** — attribute keys read directly from instrument creation/recording
  code. Key names are exact as in source.
- **Docs** — attribute tables from official documentation.
- **Not enumerated** — the source defines the instrument but no attribute list
  exists, or the instrument has no production recording call site.

## Summary

| Harness | Metric catalog | Attribute source | Notes |
| --- | --- | --- | --- |
| **Codex** | counters + histograms | Source | session tags on most instruments |
| **Claude Code** | 4 instruments | Not enumerated here | monitoring docs only |
| **openai-v2 / util-genai** | histograms | Source + Docs | GenAI semconv spec |
| **Strands** | counters + histograms | Source | TS SDK diverges from Python |
| **Cline** | 30+ instruments | Source (legacy branch) | `main` emits logs instead |
| **Cursor** | 3 delta sums | Docs | resource attrs, not datapoint attrs |
| **Hermes** | 16 gauges | Source | gateway health only |
| **Pi** | counters + histograms | Source | extension, no model label |
| **OpenCode plugin** | counters + histograms | Source | `@devtheops` extension |
| **Kilo** | none | n/a | traces + logs only |
| **OpenHands** | none exported | n/a | in-process metrics only |

## Codex

Verified from source (repo clone commit `4f39251`): `codex-rs/otel` crate plus
core call sites. Session metrics carry a fixed tag set appended to every
datapoint; global-client metrics carry none.

**Session tags** (appended to every session metric): `auth_mode` (optional),
`session_source`, `originator`, `service_name` (optional), `model`,
`app.version`. Global-client metrics (`codex.mcp.tools.*`,
`codex.remote_models.*`, `codex.plugins.startup_sync`) get no session tags.

| Metric | Type | Attributes (beyond session tags) |
| --- | --- | --- |
| `codex.thread.started` | Counter | `is_git` |
| `codex.conversation.turn.count` | Counter | none explicit |
| `codex.turn.network_proxy` | Counter | `active`, `tmp_mem_enabled` |
| `codex.tool.call` | Counter | `tool`, `success`, `sandbox`, `sandbox_policy`; MCP tools add `mcp_server`, `mcp_server_origin` |
| `codex.tool.unified_exec` | Counter | `tty` |
| `codex.websocket.request` | Counter | `success` |
| `codex.websocket.event` | Counter | `kind` (WS message type, e.g. `response.completed`, or `parse_error`/`unknown`), `success` |
| `codex.plugins.startup_sync` | Counter | `transport` = git/http/export_archive, `status` = success/failure; no session tags |
| `codex.turn.e2e_duration_ms` | Histogram | none explicit |
| `codex.turn.ttft.duration_ms` | Histogram | none explicit |
| `codex.turn.ttfm.duration_ms` | Histogram | none explicit |
| `codex.turn.token_usage` | Histogram | `token_type` = total/input/cached_input/cache_write_input/output/reasoning_output, `tmp_mem_enabled` |
| `codex.turn.tool.call` | Histogram | `tmp_mem_enabled` |
| `codex.tool.call.duration_ms` | Histogram | `tool`, `success`, `sandbox`, `sandbox_policy` (+ `mcp_server`, `mcp_server_origin` for MCP) |
| `codex.websocket.request.duration_ms` | Histogram | `success` |
| `codex.mcp.tools.list.duration_ms` | Histogram | `cache` = hit/miss; no session tags |
| `codex.mcp.tools.cache_write.duration_ms` | Histogram | `status` = success/failure; no session tags |
| `codex.mcp.tools.fetch_uncached.duration_ms` | Histogram | `trigger` = initial/explicit (only when `is_codex_apps_mcp_server`), else none; no session tags |
| `codex.startup_prewarm.duration_ms` | Histogram | `status` = cancelled/ready/failed or unavailable status |
| `codex.remote_models.load_cache.duration_ms` | Histogram | none; no session tags |
| `codex.shell_snapshot.duration_ms` | Histogram | `success` |
| `codex.session_started` | Counter | **Not enumerated** — README example only, no production call site |
| `codex.request_latency` | Histogram | **Not enumerated** — README example only, no production call site |

The Last9 Codex doc's attribute keys are stale against this source revision:
it says `type` for token usage where source uses `token_type`, `mode` for
network_proxy where source uses `active`/`tmp_mem_enabled`, and `tool.name`
where source uses `tool`. Source keys above are authoritative.

## Claude Code

Claude Code emits four instruments under meter scope
`com.anthropic.claude_code`. This pass did not capture their full attribute
sets from source; the monitoring docs define the catalog and the `type`
dimension for token usage.

| Metric | Type | Attributes |
| --- | --- | --- |
| `claude_code.session.count` | Sum (delta) | not enumerated in this pass |
| `claude_code.token.usage` | not enumerated | carries a `type` dimension |
| `claude_code.cost.usage` | not enumerated | not enumerated in this pass |
| `claude_code.lines_of_code.count` | not enumerated | not enumerated in this pass |

Enable with `CLAUDE_CODE_ENABLE_TELEMETRY=1` and
`OTEL_METRICS_EXPORTER=otlp`.

## openai-v2 / util-genai

Two emission modes. Default mode (pinned to semconv v1.30.0) records metrics
from `opentelemetry-instrumentation-openai-v2`; the experimental mode
(`OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental`) records them from
`opentelemetry-util-genai`.

### Default mode (Source)

The table records the semconv v1.30.0 spellings this mode pins to; the current
GenAI registry renames `gen_ai.system` to `gen_ai.provider.name` and moves the
service tier into the `openai.` namespace.

| Metric | Type | Attributes |
| --- | --- | --- |
| `gen_ai.client.operation.duration` | Histogram (s) | `gen_ai.operation.name`, `gen_ai.system` (=`openai`), `gen_ai.request.model` + conditional `error.type`, `gen_ai.response.model`, `gen_ai.openai.response.service_tier`, `gen_ai.openai.response.system_fingerprint`, `server.address`, `server.port` |
| `gen_ai.client.token.usage` | Histogram ({token}) | common attrs above + `gen_ai.token.type` = `input` \| `completion` |
| `gen_ai.client.operation.embeddings.duration` | Histogram (s) | common attrs above + `gen_ai.embeddings.dimension.count` |
| `gen_ai.client.operation.embeddings.usage` | Histogram ({token}) | common attrs above + `gen_ai.embeddings.dimension.count` + `gen_ai.token.type` |

### Experimental / util-genai mode (Source)

| Metric | Type | Attributes |
| --- | --- | --- |
| `gen_ai.client.operation.duration` | Histogram (s) | `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.provider.name`, `server.address`, `server.port`, `gen_ai.response.model`, + invocation metric attributes; `error.type` added on error |
| `gen_ai.client.token.usage` | Histogram ({token}) | same base set + `gen_ai.token.type` = `input` \| `output` |
| `gen_ai.client.operation.time_to_first_chunk` | Histogram (s) | same base set (recorded on first stream chunk); python-contrib util does not ship this |
| `gen_ai.client.operation.time_per_output_chunk` | Histogram (s) | same base set (recorded on later stream chunks); python-contrib util does not ship this |

The GenAI semconv spec (Docs) sets requirements for these four metrics:
`gen_ai.operation.name` Required; `gen_ai.provider.name` Required
(token.usage / time_to_first_chunk / time_per_output_chunk) or Conditionally
Required (operation.duration); `gen_ai.request.model` Conditionally Required;
`server.port` Conditionally Required if `server.address` set;
`gen_ai.response.model` Recommended; `server.address` Recommended;
`gen_ai.token.type` Required (token.usage); `error.type` Conditionally Required
on error (operation.duration).

## Strands

Verified from source (`strands-py/src/strands/telemetry/metrics_constants.py`,
`metrics.py`, `event_loop.py`, `config.py`).

| Metric | Type | Attributes/Dimensions |
| --- | --- | --- |
| `strands.event_loop.cycle_count` | Counter | `event_loop_cycle_id` (when the caller passes attrs) |
| `strands.event_loop.start_cycle` | Counter | `event_loop_cycle_id` |
| `strands.event_loop.end_cycle` | Counter | `event_loop_cycle_id` (sometimes absent — see note) |
| `strands.event_loop.cycle_duration` | Histogram | `event_loop_cycle_id` (sometimes absent — see note) |
| `strands.event_loop.latency` | Histogram | none (recorded with no attributes) |
| `strands.model.time_to_first_token` | Histogram | none (recorded with no attributes) |
| `strands.tool.call_count` | Counter | `tool_name`, `tool_use_id` |
| `strands.tool.success_count` | Counter | `tool_name`, `tool_use_id` |
| `strands.tool.error_count` | Counter | `tool_name`, `tool_use_id` |
| `strands.tool.duration` | Histogram | `tool_name`, `tool_use_id` |
| `strands.event_loop.input.tokens` | Histogram | none |
| `strands.event_loop.output.tokens` | Histogram | none |
| `strands.event_loop.cache_read.input.tokens` | Histogram | none |
| `strands.event_loop.cache_write.input.tokens` | Histogram | none |

The normal event-loop path calls `end_cycle(..., attributes={"event_loop_cycle_id": ...})`,
but the tool-execution and interrupt paths call `end_cycle(...)` with no
attributes, so `end_cycle`/`cycle_duration` datapoints are sometimes
attribute-less. Token and latency histograms always record with no attributes.
Resource attributes (meter level): `service.name`, `service.version`,
`telemetry.sdk.name`, `telemetry.sdk.language`; no meter-level default
attributes.

The TS SDK (`strands-ts/src/telemetry/meter.ts`) uses different instrument
names — `gen_ai.agent.cycle.count`, `gen_ai.agent.invocation.count`,
`gen_ai.agent.cycle.duration`, `gen_ai.agent.tool.call.count`,
`gen_ai.agent.tool.error.count`, `gen_ai.agent.tool.duration`,
`gen_ai.agent.tokens.input`, `gen_ai.agent.tokens.output`,
`gen_ai.agent.model.latency`, `gen_ai.server.time_to_first_token` — most
recorded with no attributes; tool metrics carry `gen_ai.tool.name`.

## Cline

Verified from source (clone HEAD `e7ed291` on `main`, plus the
`legacy-extension` branch). **On `main` the classic metric-recording methods
no longer exist**: only `cline.errors.*`, `cline.hooks.*`, `cline.ai_output.*`,
`cline.grpc.response.size_bytes`, and `cline.migration.*` have live metric
call sites. The turns/tokens/cost/cache/tools/api/workspace metrics are still
defined in the `METRICS` const but have no recording call site on `main` —
those signals now flow as OTel log events from the SDK core. Full attribute
sets exist on the `legacy-extension` branch.

**Standard attributes merged into every metric** (`getStandardAttributes`):
`extension_version`, `platform`, `platform_version`, `cline_type`, `os_type`,
`os_version`, `is_remote_workspace`, `is_dev` (+ conditional `device_id`,
`userId`, `organization_id`, `organization_name`, `member_id`,
`host_plugin_version`, `extension_variant`). The OTel provider stringifies
`undefined` attribute values to the literal `"undefined"`.

| Metric | Type | Attributes (on top of standard attrs) | Recorded on |
| --- | --- | --- | --- |
| `cline.turns.total` | counter | `ulid`, `provider`, `model`, `source`, `mode` | legacy only |
| `cline.turns.per_task` | histogram | `ulid`, `provider`, `model`, `source`, `mode` | legacy only |
| `cline.tokens.input.total` / `.output.total` | counter | `ulid`, `provider`, `model` | legacy only |
| `cline.tokens.input.per_response` / `.output.per_response` | histogram | `ulid`, `provider`, `model` | legacy only |
| `cline.cost.total` / `cline.cost.per_event` | counter / histogram | `ulid`, `provider`, `model`, `currency`="USD" (+ `mode` on conversation-turn path) | legacy only |
| `cline.cache.write.tokens.total` / `cline.cache.read.tokens.total` | counter | `ulid`, `provider`, `model` (+ `mode` on conversation-turn path) | legacy only |
| `cline.cache.hits.total` | counter | `ulid`, `model`, `provider`="gemini" (only when `cacheHit`) | legacy only |
| `cline.tool.calls.total` / `.per_task` | counter / histogram | `ulid`, `tool`, `model`, `success`, `autoApproved` | legacy only |
| `cline.errors.total` / `cline.errors.per_task` | counter / histogram | `ulid`, `model`, `provider`, `error_status`, `error_type`, `failure_phase` (+ conditional `error_class` on legacy) | both |
| `cline.api.ttft.seconds` | histogram | `ulid`, `provider`, `model`, `apiFormat`, `mode` (gemini path: `ulid`, `model`, `provider`="gemini") | legacy only |
| `cline.api.duration.seconds` | histogram | `ulid`, `provider`, `model`, `apiFormat`, `scope`="task", `mode` (or gemini path) | legacy only |
| `cline.api.throughput.tokens_per_second` | histogram | `ulid`, `model`, `provider`="gemini" | legacy only |
| `cline.hooks.executions.total` | counter | `ulid`, `hookName`, `status`, conditional `source`, conditional `toolName` | both |
| `cline.hooks.duration.seconds` | histogram | same hook attrs | both |
| `cline.hooks.failures.total` | counter | hook attrs + `errorType` (defaults "unknown") | both |
| `cline.hooks.cancellations.total` | counter | hook attrs | both |
| `cline.hooks.context_modifications.total` | counter | hook attrs | both |
| `cline.hooks.cache.accesses.total` | counter | `hookName`, `cacheHit` = "true"/"false" | both |
| `cline.ai_output.accepted/rejected.lines_added/deleted/changed.total` | counter | `ulid`, `tool`, `provider`, `model`, `source` | both |
| `cline.ai_output.accepted/rejected.files_created/deleted/moved.total` | counter | `ulid`, `tool`, `provider`, `model`, `source` (conditional on truthy count) | both |
| `cline.grpc.response.size_bytes` | histogram | `service`, `method`, conditional `request_id` | both |
| `cline.workspace.active_roots` | gauge | `is_multi_root` | legacy only |
| `cline.cache.write/read.tokens.per_event` | histogram | `ulid`, `provider`, `model` | legacy only |
| `cline.migration.*` (10 metrics) | counter/histogram/gauge | `migration_type`="legacy_task_to_sdk_session", `outcome`, `reason` | main only |

## Cursor

Verified from official docs (full attribute tables transcribed). All three
metrics are monotonic delta Sums.

| Metric | Type | Attributes |
| --- | --- | --- |
| `cursor.token.usage` | Monotonic delta Sum, `{token}` | `cursor.token.type` (Always: input/output/cache_read/cache_creation), `cursor.model.name` (Optional, routed-intent collapsed), `cursor.api.status` (Optional: success/errored/aborted), `cursor.api.billable` (Optional bool) |
| `cursor.tool.calls` | Monotonic delta Sum, `{call}`, value 1 | `cursor.tool.kind` (Always: builtin/mcp), `cursor.tool.name` (Always), `cursor.tool.status` (Always: success/failure/aborted), `cursor.mcp.server.name` (MCP only) |
| `cursor.cost.usage` | Monotonic delta Sum, USD | `cursor.model.name` (Optional, same collapse rules) |

Resource attributes (not datapoint attrs): `service.name`="cursor",
`service.version`, `cursor.team.id` (Always, int), `cursor.surface`,
`cursor.entrypoint`, `cursor.user.id`. No `cursor.session.id` attribute exists
on metrics — metric datapoints carry no correlation IDs; those appear on logs
only.

## Hermes

Verified from source (tarball of `main`): `agent/monitoring/gateway_health.py`,
`gateway_health_export.py`, `cron_health.py`. The only OTel instrument creation
in the tree is `meter.create_observable_gauge(...)`; all 16 metrics are
observable gauges, no counters/histograms.

**Base attribute set** (all gateway/platform metrics): `service.instance.id`
(sha256 hashed), `service.version`, `hermes.supervision_mode`
(systemd|s6|container|launchd|manual|unknown).

| Metric | Type | Attributes |
| --- | --- | --- |
| `hermes.gateway.up` | gauge | base |
| `hermes.gateway.state` | gauge | base + `hermes.gateway.state` (value always 1) |
| `hermes.gateway.active_agents` | gauge | base |
| `hermes.gateway.busy` | gauge | base |
| `hermes.gateway.drainable` | gauge | base |
| `hermes.gateway.restart_requested` | gauge | base |
| `hermes.gateway.background_work` | gauge | base |
| `hermes.gateway.background_delegations` | gauge | base |
| `hermes.platform.up` | gauge | base + `hermes.platform`, `hermes.platform.state` |
| `hermes.platform.degraded` | gauge | base + `hermes.platform`, `hermes.platform.state`, `hermes.error_code` (bucket: auth_failed/rate_limited/timeout/network_error/invalid_config/startup_failed/platform_fatal/unknown) |
| `hermes.cron.scheduler.heartbeat_age_seconds` | gauge | none (`{}`) |
| `hermes.cron.scheduler.last_success_age_seconds` | gauge | none (`{}`) |
| `hermes.cron.scheduler.catch_up_occurrences` | gauge | none (`{}`) |
| `hermes.cron.jobs.enabled` | gauge | none (`{}`) |
| `hermes.cron.jobs.running` | gauge | none (`{}`) |
| `hermes.cron.jobs.overdue` | gauge | none (`{}`) |

Resource attributes (all 16 gauges): `service.name`="hermes-gateway",
`service.instance.id`, `telemetry.scope`="gateway_health", plus
config-supplied allowlisted keys (`service.namespace`, `service.version`,
`deployment.environment.name`, `cloud.provider`, `cloud.platform`,
`cloud.region`). The `profile` value enters `_base_attrs` but is deliberately
not exported. The `hermes_cli/observability/shared_metrics.py` counters are
SQLite-backed JSON packages, not OTel instruments.

## Pi

Verified from source (`mprokopov/pi-otel-telemetry`, `/tmp/pi-otel/index.ts`,
single source file). **No metric carries an `llm.model` label** — the source
comment says "no model label to avoid series fragmentation"; `llm.model` is a
trace span attribute only. This contradicts the npm README (which lists
`llm.model` labels) and refines the GitHub README (which omits the common
attrs).

**Common attrs on every metric** (`commonMetricAttrs`): `user.name` (always),
`host.name` (always), `environment` (only if `OTEL_RESOURCE_ATTRIBUTES`
contains `environment=...`).

| Metric | Type | Labels |
| --- | --- | --- |
| `pi.tokens.input` | Counter | `user.name`, `host.name`, `environment`? |
| `pi.tokens.output` | Counter | `user.name`, `host.name`, `environment`? |
| `pi.tool.calls` | Counter | `tool.name` + common |
| `pi.tool.errors` | Counter | `tool.name` + common (only when `isError`) |
| `pi.tool.duration` | Histogram (ms) | `tool.name` + common |
| `pi.turns` | Counter | `user.name`, `host.name`, `environment`? |
| `pi.prompts` | Counter | `user.name`, `host.name`, `environment`? |
| `pi.session.duration` | Histogram (s) | `user.name`, `host.name`, `environment`? |

`tool.name` is the raw tool name from the pi event (e.g. `bash`, `read`,
`write`, `edit`).

## OpenCode plugin

Verified from source (`@devtheops/opencode-plugin-otel`, `src/otel.ts` +
handler files). This is the extension path; native OpenCode OTel is
traces-focused and the docs cover it in `docs/harnesses.md`.

**Common attributes on every metric point** (`ctx.commonAttrs`): `project.id`
(always) + every user-configured `OPENCODE_SPAN_ATTRIBUTES` `key=value` pair.
Every metric also carries `session.id`. Metric prefix `opencode.` is
configurable via `OPENCODE_METRIC_PREFIX`.

| Metric | Type | Attributes |
| --- | --- | --- |
| `opencode.session.count` | Counter | `session.id`, `is_subagent` (bool) |
| `opencode.token.usage` | Counter | `session.id`, `model`, `agent`, `type` ∈ {input, output, reasoning, cacheRead, cacheCreation} |
| `opencode.cost.usage` | Counter | `session.id`, `model`, `agent` |
| `opencode.lines_of_code.count` | Counter | `session.id`, `type` ∈ {added, removed} |
| `opencode.lines_of_code.total` | Gauge | `session.id`, `type` ∈ {added, removed} |
| `opencode.commit.count` | Counter | `session.id` |
| `opencode.tool.duration` | Histogram | `session.id`, `tool_name`, `success` (bool) |
| `opencode.cache.count` | Counter | `session.id`, `model`, `agent`, `type` ∈ {cacheRead, cacheCreation} |
| `opencode.session.duration` | Histogram | `session.id` |
| `opencode.message.count` | Counter | `session.id`, `model`, `agent` |
| `opencode.session.token.total` | Histogram (var named `sessionTokenGauge` but is a Histogram) | `session.id` |
| `opencode.session.cost.total` | Histogram (var named `sessionCostGauge` but is a Histogram) | `session.id` |
| `opencode.model.usage` | Counter | `session.id`, `model`, `provider`, `agent` |
| `opencode.retry.count` | Counter | `session.id` |
| `opencode.subtask.count` | Counter | `session.id`, `agent`, `agent.type`="subagent" |

## No OTel metrics

**Kilo** — the docs' OpenTelemetry section covers traces and logs only
(request spans with `http.method`, `http.path`, route params `session.id`/
`message.id`, internal `opencode.*` params). No counters/histograms/gauges,
no `/v1/metrics`, no metric exporter mentioned anywhere. Confirmed: no OTel
metrics for the Kilo CLI.

**OpenHands** — metrics are in-process Python objects only: `llm.metrics`
(`accumulated_cost`, `accumulated_token_usage` with prompt/completion/
cache_read/cache_write/reasoning tokens + `context_window`, `costs`,
`token_usages`, `response_latencies`) and `conversation.conversation_stats`.
No OTLP metrics export. Tracing goes through Laminar (OTel instrumentation
layer).

## Doc-vs-source disagreements

- **Pi**: the npm README lists `llm.model` labels on metrics; the source
  omits the label entirely (trace-span-only attribute).
- **Codex**: the Last9 doc's keys are stale — `type` vs source `token_type`,
  `mode` vs `active`/`tmp_mem_enabled`, `tool.name` vs `tool`.
- **Cursor**: `cursor.team.id` is a resource attribute, not a datapoint
  attribute, and no `cursor.session.id` exists on metrics.
- **Cline**: the npm/GitHub README catalog implies the turns/tokens/cost/
  cache/tools/api/workspace metrics record on `main`; the source deletes
  those call sites and emits the data as log events instead.

## Sources

- Codex OTel: https://github.com/openai/codex (codex-rs/otel/README.md,
  codex-rs/otel/src/metrics/config.rs, codex-rs/core call sites)
- Claude Code monitoring: https://code.claude.com/docs/en/monitoring-usage
- openai-v2 instrumentation:
  https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2
- util-genai:
  https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/util/opentelemetry-util-genai
- GenAI semconv metrics: https://github.com/open-telemetry/semantic-conventions-genai
- Strands metrics: https://strandsagents.com/docs/user-guide/observability-evaluation/metrics/
  and https://github.com/strands-agents/sdk-python (strands-py/src/strands/telemetry/)
- Cline telemetry source: https://github.com/cline/cline/blob/main/src/services/telemetry
- Cursor OTel Wire Reference: https://cursor.com/docs/enterprise/opentelemetry-export/wire
- Hermes Agent: https://github.com/NousResearch/hermes-agent (agent/monitoring/)
- Pi extension: https://github.com/mprokopov/pi-otel-telemetry
- OpenCode plugin: https://github.com/DEVtheOPS/opencode-plugin-otel
- Kilo CLI: https://kilo.ai/docs/code-with-ai/platforms/cli
- OpenHands metrics: https://docs.openhands.dev/sdk/guides/metrics