# End-to-end tests

The live e2e tests build the custom Collector, run a real coding agent in a
container, and check the exported OTLP traces on the host with
`go test -tags=e2e ./e2e/validator`. They call real models and incur API cost, so
they are opt-in and never run in CI.

The stacks share `compose.e2e-base.yaml` (the collector); each defines only
its own `agent` service. Each stack writes output under `.e2e-output/`.
Adding a stack is automatic in CI and `scripts/check.sh` (globs cover the
scripts, run.sh files, compose files, and Dockerfiles); the only per-stack
work is its credential-split assertion.

The stacks use one of three model APIs. Claude Code runs against z.ai's
Anthropic-compatible endpoint (the Copilot CLI stack defaults there too).
The OpenAI Completions-compatible stacks point at either
[z.ai](https://docs.z.ai/)'s GLM models or OpenCode Go (the OpenCode stack).
OpenHands reaches its model directly through litellm and defaults to
Anthropic's API with model `anthropic/claude-sonnet-4-5`.
Nothing about the connector is provider-specific — these tests simply connect
to whatever each stack points at. A single z.ai API key covers every stack
except OpenCode (which needs its own `OPENCODE_API_KEY`) and OpenHands (whose
`LLM_API_KEY` must be valid for whatever provider `LLM_MODEL` names — an
Anthropic API key by default).

## Live Codex E2E

The Compose stack builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and checks the exported OTLP JSON. It verifies the root/child
hierarchy, canonical attributes, completion state, and absence of sensitive
copied fields.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `OPENAI_API_KEY` holding a z.ai API key;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENAI_API_KEY=...   # your z.ai key
./scripts/e2e.sh
```

Optional overrides:

```bash
CODEX_VERSION=0.149.0 E2E_CODEX_MODEL=glm-4.7 E2E_AGENT_TIMEOUT=10m ./scripts/e2e.sh
```

The E2E defaults to `glm-4.7`, the model the pinned connector regression
captures used. Overriding `E2E_CODEX_MODEL` also means adding an entry to
`e2e/responses-proxy/config.yaml`, because the proxy routes by model name and
only serves the models listed there.

### The responses-proxy

Codex 0.144.1 dropped `wire_api = "chat"` and speaks only the Responses API, which
z.ai's coding endpoint does not serve. A third service, `responses-proxy`,
sits between them and translates Responses to Chat Completions. The proxy
builds from a pinned commit on a fork (see `e2e/responses-proxy/Dockerfile` for
why the published release is unusable here) and exists only for this test — it
is not part of the connector or any production path.

It also injects `stream_options.include_usage` into the outbound request, because
z.ai otherwise omits token usage from the stream. Without that the canonical chat
spans still appear, but without `gen_ai.usage.*` attributes, and the validator
requires usage on the Codex path.

### Credentials and sandboxing

`responses-proxy` is the only container that receives the real z.ai key. The Codex
container gets a placeholder: `config.toml` names `OPENAI_API_KEY` as its
`env_key` and Codex refuses to start when that variable is unset, but the value
only travels to the proxy as a bearer token, and the proxy accepts any token
(`auth.keys` is empty). CI asserts this split.

Codex runs with `--sandbox danger-full-access` and `approval_policy = "never"`.
Its inner bubblewrap sandbox cannot create the user namespace it needs inside an
unprivileged container, so the container itself is the isolation boundary: it
mounts no host path and holds no real credential. Model-issued commands do get
filesystem writes and network egress within that container, so treat the Codex
e2e as running untrusted commands in a throwaway sandbox — not as a hardened one.

To inspect or drive the stack manually, bring up both long-running services and
keep them up so the collector's file exporter can flush while validation polls:

```bash
export E2E_RUN_ID="manual-$(date +%s)"
export OPENAI_API_KEY=...
docker compose -f compose.e2e-codex.yaml up --detach --wait collector responses-proxy
docker compose -f compose.e2e-codex.yaml run --rm --no-deps agent
TRACE_FILE="$PWD/.e2e-output/canonical-traces.json" go test -tags=e2e ./e2e/validator/
docker compose -f compose.e2e-codex.yaml down
```

## Live Claude Code E2E

The Claude stack runs pinned Claude Code in bare, non-interactive mode against
z.ai's Anthropic-compatible endpoint, and requires exactly one Bash tool
invocation. The raw trace pipeline preserves native beta traces,
`coding_agent/claude` normalizes them, and the host checks both together. Prompt
text, tool arguments, tool output, and raw API bodies remain disabled.

### Credentials: one z.ai API key

The container receives exactly one credential, `ANTHROPIC_AUTH_TOKEN`, and
`ANTHROPIC_BASE_URL` points Claude Code at `https://api.z.ai/api/anthropic`.
`ANTHROPIC_API_KEY` is deliberately never set, so it cannot shadow the auth token;
CI asserts both facts. Treat local Docker access as privileged, since Docker
operators can inspect container environments.

```bash
export ANTHROPIC_AUTH_TOKEN=...   # your z.ai key
./scripts/e2e-claude.sh
```

The test defaults to `glm-4.7`, at most three agent turns, a $0.25 budget
ceiling, and a ten-minute timeout. Claude Code maps its model tiers onto GLM
models: `E2E_CLAUDE_MODEL` sets both the main and Sonnet tiers, and
`E2E_CLAUDE_HAIKU_MODEL` (default `glm-4.5-air`) covers the Haiku tier used for
background requests.

```bash
E2E_CLAUDE_MODEL=glm-4.7 E2E_CLAUDE_HAIKU_MODEL=glm-4.5-air ./scripts/e2e-claude.sh
```

## Live OpenAI-SDK ad-hoc agent (paid)

The OpenAI-SDK stack uses a pinned Python image with pinned `openai`,
`opentelemetry-instrumentation-openai-v2`, SDK, and OTLP exporter packages. A
small ad-hoc agent script makes one chat-completions call to z.ai's
OpenAI-compatible endpoint (no responses-proxy needed, unlike Codex) and
force-flushes. The script runs twice in one paid container run, once per
convention mode, under distinct service names. Validation checks rootless
normalized `chat` spans for both modes, provider `openai`, usage tokens, and
no content keys.

```bash
export OPENAI_API_KEY=...   # your z.ai key
./scripts/e2e-openai.sh
```

The first run uses default semconv v1.30.0 under service name
`openai-adhoc-legacy`; the second run sets
`OTEL_SEMCONV_STABILITY_OPT_IN=genai_latest_experimental` under
`openai-adhoc-latest`. Override the model with `E2E_OPENAI_MODEL` (default
`glm-4.7`):

```bash
E2E_OPENAI_MODEL=glm-4.7 ./scripts/e2e-openai.sh
```

## Live Strands agent (paid)

The Strands stack uses a pinned `strands-agents` image with its
OpenAI-compatible model provider pointed at z.ai and one harmless tool the
prompt forces. The raw trace pipeline preserves the native tree,
the GenAI normalizer produces the canonical `invoke_agent`/`chat`/`execute_tool`
tree, and the host checks both outputs. Content events appear in raw output
and are absent in canonical output.

```bash
export OPENAI_API_KEY=...   # your z.ai key
./scripts/e2e-strands.sh
```

Override the model with `E2E_STRANDS_MODEL`:

```bash
E2E_STRANDS_MODEL=glm-4.7 ./scripts/e2e-strands.sh
```

## Live OpenCode E2E

The OpenCode stack builds the custom Collector and runs a real non-interactive
`opencode run` against the [OpenCode Go](https://opencode.ai/zen) plan through
the provider alias `oclaude`, using the default model `ox-alpha-free`. The
prompt forces one harmless bash tool call. Native OTLP export activates through
`experimental.openTelemetry` in the baked-in `opencode.json` plus
`OTEL_EXPORTER_OTLP_ENDPOINT`, so spans stream to the collector while the agent
runs. The stack then waits for trace normalization and validates the exported
OTLP JSON on the host.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `OPENCODE_API_KEY` holding an OpenCode Go API key;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENCODE_API_KEY=...   # your OpenCode Go API key
./scripts/e2e-opencode.sh
```

Optional overrides:

```bash
OPENCODE_VERSION=1.18.21 E2E_OPENCODE_MODEL=ox-alpha-free E2E_AGENT_TIMEOUT=10m ./scripts/e2e-opencode.sh
```

`E2E_OPENCODE_MODEL` must name a model that exists in the `models` map of
`e2e/opencode/opencode.json`; the baked-in config has no other entries.

### Fixture refresh

A successful run leaves raw OTLP JSON under `.e2e-output/raw-traces.json`. The
committed regression fixture,
`connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json`,
comes from slicing that file. The first jq command keeps the first resource group
whose scope contains an `ai.streamText` subtree (its Effect-noise siblings come
along, which the replay test needs); the second keeps every `ai.*` span and
samples at most 20 noise spans per scope so the fixture stays small:

```bash
jq -s '{resourceSpans: ([.[] | .resourceSpans[]?] | map(select(any(.scopeSpans[]?; any(.spans[]?; .name == "ai.streamText")))))[:1]}' \
  .e2e-output/raw-traces.json > /tmp/opencode/fixture-full.json
jq '.resourceSpans[0].scopeSpans |= map(.spans |= ((map(select(.name | startswith("ai.")))) + ([.[] | select((.name | startswith("ai.") | not))][:20])))' \
  /tmp/opencode/fixture-full.json \
  > connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json
```

After slicing, rerun `go test ./internal/opencode/` from
`connector/codingagentconnector/` to confirm the fixture still replays green.

## Live Pi agent (paid)

The Pi stack runs the [Pi coding agent](https://pi.dev) with the
`@amaster.ai/pi-telemetry` extension (pinned via image args), which exports
OTLP/HTTP traces to the shared collector. The image bakes the telemetry
settings and a `models.json` provider pointing at z.ai's
Anthropic-compatible endpoint; `ANTHROPIC_AUTH_TOKEN` is the only credential.
The prompt forces one bash tool call. The run produces two agentic iterations,
so validation accepts any valid `invoke_agent pi` root with chat and
`execute_tool` children.

```bash
export ANTHROPIC_AUTH_TOKEN=...   # your z.ai key
./scripts/e2e-pi.sh
```

Override the model with `E2E_PI_MODEL` (default `zai/glm-4.7`) or pin versions
with `PI_CODING_AGENT_VERSION`:

```bash
E2E_PI_MODEL=zai/glm-4.7 ./scripts/e2e-pi.sh
```

## Live Copilot CLI E2E

The Copilot stack builds the custom Collector and runs a real non-interactive
`copilot -p` session against a BYOK provider configured through environment
variables. The run needs no GitHub authentication or Copilot subscription;
charges land only on the provider account behind the key. Native telemetry activates
through `COPILOT_OTEL_ENABLED` plus `OTEL_EXPORTER_OTLP_ENDPOINT`, so spans
stream to the collector while the CLI runs. The prompt forces one harmless
shell tool call. Validation accepts any valid `invoke_agent` root (the
agent-name subject is producer-chosen) carrying conversation id and token
usage, plus chat and `execute_tool` children, and rejects GenAI content in
canonical output.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- a `COPILOT_PROVIDER_API_KEY` holding an API key for the configured provider;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export COPILOT_PROVIDER_API_KEY=...   # your z.ai key
./scripts/e2e-copilot.sh
```

The default points at z.ai's Anthropic-compatible endpoint with model
`glm-4.7`. Any OpenAI Completions-compatible provider works through the
override variables, for example:

```bash
E2E_COPILOT_PROVIDER_TYPE=openai \
E2E_COPILOT_BASE_URL=https://api.openai.com/v1 \
E2E_COPILOT_MODEL=gpt-5.2 \
./scripts/e2e-copilot.sh
```

Pin the CLI version with `COPILOT_CLI_VERSION`.

### Fixture capture

A successful run leaves raw OTLP JSON under `.e2e-output/raw-traces.json`, the
same flow the OpenCode stack documents. Captured traces feed the committed
connector fixtures: the GenAI edge's
`internal/genai/testdata/copilot-native.otlp.json` originated from the
documented schema, and real captures are how that fixture gets refreshed
against actual CLI output. After updating it, rerun
`go test ./internal/genai/` from `connector/codingagentconnector/` to confirm
the fixture still replays green.

## Live OpenHands E2E

The OpenHands stack builds the custom Collector and runs a real headless
[OpenHands SDK](https://github.com/OpenHands/software-agent-sdk) conversation
(pinned via the `OPENHANDS_SDK_VERSION` image arg) with one terminal tool the
prompt forces. The agent reaches its model directly through litellm — no
proxy service — with default model `anthropic/claude-sonnet-4-5`.
The SDK's Laminar instrumentation exports OTLP/HTTP traces through
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` plus
`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf`, so spans stream to the
collector while the conversation runs. The stack then waits for trace
normalization and validates the exported OTLP JSON on the host.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `LLM_API_KEY` holding a key for whatever provider `LLM_MODEL` names
  (an Anthropic API key for the default model);
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export LLM_API_KEY=...   # key for the provider behind LLM_MODEL
./scripts/e2e-openhands.sh
```

Optional overrides:

```bash
OPENHANDS_SDK_VERSION=1.43.1 E2E_AGENT_TIMEOUT=10m ./scripts/e2e-openhands.sh
```

Override the model with `LLM_MODEL`; `LLM_API_KEY` must stay valid for the
provider that model routes to.

### Fixture refresh

A successful run leaves raw OTLP JSON under `.e2e-output/raw-traces.json`, the
same flow the OpenCode stack documents. Captured traces feed the committed
connector fixtures under
`internal/openhands/testdata/`; after updating them, rerun
`go test ./internal/openhands/` from `connector/codingagentconnector/` to
confirm the fixtures still replay green.
