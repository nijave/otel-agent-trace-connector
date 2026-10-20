# End-to-end tests

The live e2e tests build the custom Collector, run a real coding agent in a
container, and check the exported OTLP traces on the host with
`go test -tags=e2e ./e2e/validator`. They call real models and incur API cost, so
they are opt-in and never run in CI. Because of that, any change that impacts a
harness requires that harness's live e2e to run locally before opening a PR. A
change to a shared surface — canonical vocabulary/normalizers, the collector
config, the validator, `compose.e2e-base.yaml` — impacts every harness, so run
every live stack (Cursor has no live stack; its unit fixtures are the only
check). `scripts/check.sh` builds these stacks but does not exercise them, so
a local run is the only check that catches regressions in what the live
validator asserts.

The stacks share `compose.e2e-base.yaml` (the collector); each defines only
its own `agent` service. Each stack writes output under `.e2e-output/`.
Adding a stack is automatic in CI and `scripts/check.sh` (globs cover the
scripts, run.sh files, compose files, and Dockerfiles); the only per-stack
work is its credential-split assertion.

The paid stacks need exactly two host credentials. The Claude, Codex, Pi, and
OpenCode stacks use `OPENCODE_API_KEY`, an OpenCode Go key; the Copilot,
OpenHands, Strands, and OpenAI-adhoc stacks use `OPENROUTER_API_KEY`, an
OpenRouter key. OpenCode Go
(`https://opencode.ai/zen/go`) requires every request to carry a session id
and rejects requests without one; it recognizes the native session headers of
OpenCode, Claude Code, Codex, and Pi, which is why exactly those four stacks
talk to it. The OpenRouter stacks talk to OpenRouter's OpenAI-compatible
endpoint (`https://openrouter.ai/api/v1`, bearer auth). Nothing about the
connector is provider-specific — these tests simply connect to whatever each
stack points at. The routing stack replays a committed fixture and needs no
credential at all.

## Live Codex E2E

The Compose stack builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and checks the exported OTLP JSON. It verifies the root/child
hierarchy, canonical attributes, completion state, and absence of sensitive
copied fields.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `OPENCODE_API_KEY` holding an OpenCode Go API key;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENCODE_API_KEY=...   # your OpenCode Go key
./scripts/e2e.sh
```

Optional overrides:

```bash
CODEX_VERSION=0.163.0 E2E_CODEX_MODEL=gpt-5.6-luna E2E_AGENT_TIMEOUT=10m ./scripts/e2e.sh
```

The E2E defaults to `gpt-5.6-luna`, an OpenCode Go model served through the
Responses API. Codex talks to Go directly; Go recognizes the session header
Codex sends natively, so the stack runs no proxy.

### Credentials and sandboxing

The Codex container holds the real OpenCode Go key: `config.toml` names
`OPENCODE_API_KEY` as its `env_key`, and Codex sends it to OpenCode Go as the
bearer token. The container is the isolation boundary, and model-issued
commands run with full access inside it — filesystem writes and network egress
included — so treat the Codex e2e as running untrusted commands in a throwaway
sandbox, not as a hardened one. It mounts no host path, but a model-issued
command can read the key and send it anywhere over the network; the Go plan's
usage cap bounds what a leaked key can spend.

Codex runs with `--sandbox danger-full-access` and `approval_policy = "never"`.
Its inner bubblewrap sandbox cannot create the user namespace it needs inside an
unprivileged container, so the container itself is the isolation boundary.

To inspect or drive the stack manually, bring up the long-running collector and
keep it up so its file exporter can flush while validation polls:

```bash
export E2E_RUN_ID="manual-$(date +%s)"
export OPENCODE_API_KEY=...
docker compose -f compose.e2e-codex.yaml up --detach --wait collector
docker compose -f compose.e2e-codex.yaml run --rm --no-deps agent
TRACE_FILE="$PWD/.e2e-output/canonical-traces.json" go test -tags=e2e ./e2e/validator/
docker compose -f compose.e2e-codex.yaml down
```

## Live Claude Code E2E

The Claude stack runs pinned Claude Code in bare, non-interactive mode against
OpenCode Go's Anthropic-compatible endpoint, and requires exactly one Bash tool
invocation. The raw trace pipeline preserves native beta traces,
`coding_agent/claude` normalizes them, and the host checks both together. Prompt
text, tool arguments, tool output, and raw API bodies remain disabled.

### Credentials: one OpenCode Go key

The container receives exactly one credential, `ANTHROPIC_API_KEY` (mapped from
host `OPENCODE_API_KEY`), and `ANTHROPIC_BASE_URL` points Claude Code at
`https://opencode.ai/zen/go`. Go authenticates `/messages` with `x-api-key`
only — a bearer token fails — so Claude Code must receive the key as
`ANTHROPIC_API_KEY`, never `ANTHROPIC_AUTH_TOKEN`; CI asserts both facts. Treat
local Docker access as privileged, since Docker operators can inspect container
environments.

```bash
export OPENCODE_API_KEY=...   # your OpenCode Go key
./scripts/e2e-claude.sh
```

The test defaults to `claude-haiku-5-5`, at most three agent turns, a $0.25
budget ceiling, and a ten-minute timeout. Claude Code maps every model
tier (main, Sonnet, Haiku) onto `E2E_CLAUDE_MODEL`.

```bash
E2E_CLAUDE_MODEL=claude-haiku-5-5 ./scripts/e2e-claude.sh
```

## Live OpenAI-SDK ad-hoc agent (paid)

The OpenAI-SDK stack uses a pinned Python image with pinned `openai`,
`opentelemetry-instrumentation-openai-v2`, SDK, and OTLP exporter packages. A
small ad-hoc agent script makes one chat-completions call to OpenRouter's
OpenAI-compatible endpoint and force-flushes. The script runs twice in one
paid container run, once per convention mode, under distinct service names.
Validation checks rootless normalized `chat` spans for both modes, provider
`openai`, usage tokens, and no content keys.

```bash
export OPENROUTER_API_KEY=...   # your OpenRouter key
./scripts/e2e-openai.sh
```

The first run uses default semconv v1.30.0 under service name
`openai-adhoc-legacy`; the second run sets
`OTEL_SEMCONV_STABILITY_OPT_IN=genai_latest_experimental` under
`openai-adhoc-latest`. Override the model with `E2E_OPENAI_MODEL` (default
`z-ai/glm-5.3-flash`):

```bash
E2E_OPENAI_MODEL=z-ai/glm-5.3-flash ./scripts/e2e-openai.sh
```

## Live Strands agent (paid)

The Strands stack uses a pinned `strands-agents` image with its
OpenAI-compatible model provider pointed at OpenRouter and one harmless tool the
prompt forces. The raw trace pipeline preserves the native tree,
the GenAI normalizer produces the canonical `invoke_agent`/`chat`/`execute_tool`
tree, and the host checks both outputs. Content events appear in raw output
and are absent in canonical output.

```bash
export OPENROUTER_API_KEY=...   # your OpenRouter key
./scripts/e2e-strands.sh
```

Override the model with `E2E_STRANDS_MODEL`:

```bash
E2E_STRANDS_MODEL=z-ai/glm-5.3-flash ./scripts/e2e-strands.sh
```

## Live OpenCode E2E

The OpenCode stack builds the custom Collector and runs a real non-interactive
`opencode run` against the [OpenCode Go](https://opencode.ai/zen) plan through
its built-in `opencode-go` provider, using the default model `glm-5.3-flash`.
The built-in provider reads the key from `OPENCODE_API_KEY` and sends the
session header OpenCode Go requires. The prompt forces one harmless bash tool
call. Native OTLP export activates through `experimental.openTelemetry` in the
baked-in `opencode.json` plus `OTEL_EXPORTER_OTLP_ENDPOINT`, so spans stream to
the collector while the agent runs. The stack then waits for trace
normalization and validates the exported OTLP JSON on the host.

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
OPENCODE_VERSION=1.19.3 E2E_OPENCODE_MODEL=glm-5.3-flash E2E_AGENT_TIMEOUT=10m ./scripts/e2e-opencode.sh
```

`E2E_OPENCODE_MODEL` names the model inside the built-in `opencode-go`
provider (run.sh prefixes `opencode-go/`).

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
`@amaster.ai/pi-telemetry@0.1.28` extension (pinned in `e2e/pi/settings.json`), which exports
OTLP/HTTP traces to the shared collector. Pi talks to OpenCode Go through its
built-in `opencode-go` provider, which reads the key from `OPENCODE_API_KEY`
and sends the session header Go requires; the image bakes in no custom
provider config. The prompt forces one bash tool call. The run produces two
agentic iterations, so validation accepts any valid `invoke_agent pi` root with
chat and `execute_tool` children.

```bash
export OPENCODE_API_KEY=...   # your OpenCode Go key
./scripts/e2e-pi.sh
```

Override the model with `E2E_PI_MODEL` (default `opencode-go/claude-haiku-5-5`)
or pin versions with `PI_CODING_AGENT_VERSION`:

```bash
E2E_PI_MODEL=opencode-go/claude-haiku-5-5 ./scripts/e2e-pi.sh
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
export OPENROUTER_API_KEY=...   # your OpenRouter key
./scripts/e2e-copilot.sh
```

The default points at OpenRouter's OpenAI-compatible endpoint with model
`z-ai/glm-5.3-flash`. Any OpenAI Completions-compatible provider works through
the override variables, for example:

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
proxy service — with default model `openrouter/z-ai/glm-5.3-flash`
(litellm's OpenRouter prefix).
The SDK's Laminar instrumentation exports OTLP/HTTP traces through
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` plus
`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf`, so spans stream to the
collector while the conversation runs. The stack then waits for trace
normalization and validates the exported OTLP JSON on the host.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `OPENROUTER_API_KEY` holding an OpenRouter key (the default model routes
  through litellm's `openrouter/` prefix);
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENROUTER_API_KEY=...   # your OpenRouter key
./scripts/e2e-openhands.sh
```

Optional overrides:

```bash
OPENHANDS_SDK_VERSION=1.56.0 E2E_AGENT_TIMEOUT=10m ./scripts/e2e-openhands.sh
```

Override the model with `LLM_MODEL`; `LLM_API_KEY` must stay valid for the
provider that model routes to (litellm's provider prefixes, such as
`openrouter/`, need no base URL override).

### Fixture refresh

A successful run leaves raw OTLP JSON under `.e2e-output/raw-traces.json`, the
same flow the OpenCode stack documents. Captured traces feed the committed
connector fixtures under
`internal/openhands/testdata/`; after updating them, rerun
`go test ./internal/openhands/` from `connector/codingagentconnector/` to
confirm the fixtures still replay green.

## Routing E2E (zero API cost)

Unlike every stack above, this one calls no model and needs no credentials.
It builds two tier-2 replicas of the custom Collector behind a stock
`otelcol-contrib` gateway running the `load_balancing` exporter, then replays
the pinned codex fixture as `CONVERSATIONS` synthetic conversations (default
8) through the gateway. `TestRoutingAffinity` asserts each conversation lands
whole — root plus chat spans — on exactly one tier-2 replica; spread across
replicas prints to the log instead. This stack is the runnable twin of
`examples/k8s-ha/`: the same two-tier topology, with Compose standing in for
Kubernetes manifests.

```bash
GOTOOLCHAIN=auto ./scripts/e2e-routing.sh
```

Override the conversation count with `CONVERSATIONS`:

```bash
CONVERSATIONS=20 GOTOOLCHAIN=auto ./scripts/e2e-routing.sh
```
