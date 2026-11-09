# Claude Code chat spans from logs (2026-11-08)

**Goal:** Give canonical Claude Code `chat` spans the per-request cost
(`coding_agent.usage.cost`) and API failures, which only Claude Code's logs
carry, without adding state to the connector.

**Status:** design approved in chat on 2026-11-08 ("Option B: stateless
log-to-span synthesizer"). We rejected a stateful join of span and log: it
makes the Claude edge stateful, needs signal affinity in HA, and loses held
spans on a crash. Project preference: accept small data tradeoffs rather than
add state, which brings many edge cases.

## Evidence (live Claude Code 2.1.319, 2026-11-07)

- The traces wire has no cost. The `claude_code.api_request` log event has
  `cost_usd` on every record.
- Log scope `com.anthropic.claude_code.events`; body
  `claude_code.api_request` or `claude_code.api_error`; attribute
  `event.name` = `api_request` / `api_error`.
- `api_request` attributes: `request_id`, `model`, `input_tokens`,
  `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`,
  `cache_creation_5m_tokens`, `cache_creation_1h_tokens`, `cost_usd`,
  `cost_usd_micros`, `ttft_ms`, `duration_ms`, `effort`, `speed`,
  `session.id`, `prompt.id`, `agent_id`, `agent.name`, `query_source`,
  `event.timestamp`, `event.sequence`, identity keys (`user.id`,
  `user.email`, `user.account_uuid`, `user.account_id`, `organization.id`),
  `terminal.type`, and `vcs.*` / `process.working_directory`.
- `api_error` attributes: `request_id`, `model`, `error`, `status_code`
  (for example `429`), `attempt`, `duration_ms`, plus the shared keys above.
  It has no tokens, cost, or `ttft_ms`.
- Over 2 days, every `api_request` log joined to its native
  `claude_code.llm_request` span (1,202 of 1,202):
  - log `TraceId` = span `TraceId`;
  - log `SpanId` = the span's **parent** (`claude_code.interaction`, or
    `claude_code.tool.execution` for subagent requests);
  - log `request_id` = span `request_id` = span `gen_ai.response.id`;
  - log timestamp = span end time (within 1 ms).
- `cost_usd` summed over 7 days matched what the joined spans would carry
  ($398.59 over 4,335 of 4,343 requests).

## Design

### New stateless logs edge: `internal/claudelogs`

The edge claims log records whose scope is `com.anthropic.claude_code.events`
and whose `event.name` is `api_request` or `api_error`. It emits one canonical
span per record and ignores other Claude events. Register it in `logsRouter`.
It holds no state; Start and Shutdown are no-ops.

For each claimed record that has a non-zero TraceId and SpanId:

| Canonical span field | Value |
| --- | --- |
| Trace ID | log TraceId |
| Parent span ID | log SpanId |
| Span ID | first 8 bytes of SHA-256(`request_id`) (deterministic, so replays are idempotent); the edge drops records without `request_id` |
| Name | `chat <model>`, or `chat` without a model |
| Kind | Client |
| End time | log timestamp (`event.timestamp` when the record timestamp is zero) |
| Start time | end − `duration_ms` (end when `duration_ms` is missing) |
| Status | Unset for `api_request`; Error for `api_error`, message = `error` |

Attribute mapping (canonical vocabulary only; fail-closed like every edge):

| Log attribute | Canonical key |
| --- | --- |
| `model` | `gen_ai.request.model` |
| `request_id` | `gen_ai.response.id` |
| `input_tokens` | `gen_ai.usage.input_tokens` |
| `output_tokens` | `gen_ai.usage.output_tokens` |
| `cache_read_tokens` | `gen_ai.usage.cache_read.input_tokens` |
| `cache_creation_tokens` | `gen_ai.usage.cache_write.input_tokens` |
| `ttft_ms` | `gen_ai.response.time_to_first_chunk` (ms → s, double) |
| `effort` | `gen_ai.request.reasoning.level` |
| `cost_usd` | `coding_agent.usage.cost` (double; a value of `0` maps as `0`; unparsable → absent) |
| `session.id` | `gen_ai.conversation.id` |
| `agent_id` | `gen_ai.agent.id` |
| `user.id`, `user.email`, `organization.id` | `coding_agent.user.id`, `coding_agent.user.email`, `coding_agent.team.id` (only with `capture_identity`) |
| `terminal.type` | `coding_agent.terminal.type` |

Connector-written keys match the Claude traces edge: `gen_ai.operation.name` =
`chat`, `gen_ai.provider.name` = `anthropic`, `coding_agent.source` =
`native`, `coding_agent.source.event` = the body (`claude_code.api_request`
or `claude_code.api_error`), `coding_agent.client.name` = `claude_code`,
`coding_agent.client.version` = resource `service.version`. Resource
attributes go through the shared canonical resource filter. Every other log
attribute drops. Counters given as strings or integers must both parse.

The edge drops records without trace context (TraceId or SpanId all zero);
document this. Clients that export logs without traces are out of scope.

### Selecting the chat source: `claude_code.chat_source`

A new `chat_source` field in the `claude_code:` config block:

- `traces` (default; current behavior): the Claude traces edge emits `chat`
  from `claude_code.llm_request`, and the new logs edge emits nothing.
- `logs`: the Claude traces edge drops `claude_code.llm_request` from
  canonical output (interaction, tool, and sub-spans keep today's behavior), and the
  new logs edge emits `chat` spans.

Any other value fails `Validate`. Exactly one source emits `chat` spans, so
canonical output never duplicates a request. A deployment that splits logs and
traces across two connector instances must set the same value on both;
document this in the README collector configuration section.

### Accepted losses with `logs`

- `gen_ai.response.finish_reasons` (only the span has `stop_reason`).
- The chat span ID changes from the native span ID to the hash above. Nothing
  references the `llm_request` span ID in canonical output; the
  `gen_ai.request.attempt` span events on `llm_request` drop with it.

## Tests and documentation

- Unit tests for the edge: the mapping table, error records, the zero-cost
  case, string and integer counters, missing `request_id`, missing trace
  context, foreign records ignored, `capture_identity` off.
- A captured fixture with real key names and synthetic values (no real
  emails, IDs, paths, or repository names) in `internal/claudelogs/testdata/`,
  plus a conformance test like the other edges, and registration in the
  cross-harness conformance test.
- A traces-edge test: with `claude_code.chat_source: logs`, an `llm_request` span
  produces no canonical span, and its siblings still pass.
- A test that a synthesized chat span's parent is a span ID that the Claude
  traces edge preserves (the canonical interaction span keeps its native
  span ID).
- Config tests for the new field (default, both values, invalid value, YAML
  decode).
- `docs/harnesses/claude-code.md`: a Logs → Connector mapping section with
  the table above; fix the stale statements that `request_id` and
  `gen_ai.response.id` are absent and that Claude has no per-request cost.
  Update the cost row in `docs/canonical-attributes.md` (Claude Code is now a
  source) and the README.
