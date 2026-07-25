# End-to-end tests

The live e2e tests build the custom Collector, run a real coding agent in a
container, and validate the exported OTLP traces on the host with
`go test -tags=e2e ./e2e/validator`. They call real models and incur API cost, so
they are opt-in and never run in CI.

Both stacks share `compose.e2e-base.yaml` (the collector); each defines only its
own `agent` service. Output is written under `.e2e-output/`.

Both agents run against [z.ai](https://docs.z.ai/)'s GLM models, so a single z.ai
API key covers both stacks. Nothing about the connector is z.ai-specific — it is
simply the provider these tests are wired to.

## Live Codex E2E

The Compose stack builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and validates the exported OTLP JSON. It checks the root/child
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
CODEX_VERSION=0.144.1 E2E_CODEX_MODEL=glm-4.7 E2E_AGENT_TIMEOUT=10m ./scripts/e2e.sh
```

The E2E defaults to `glm-4.7`, the model the pinned connector regression captures
were recorded against. Overriding `E2E_CODEX_MODEL` also means adding an entry to
`e2e/responses-proxy/config.yaml`, because the proxy routes by model name and
only serves the models listed there.

### The responses-proxy

Codex 0.144.1 dropped `wire_api = "chat"` and speaks only the Responses API, which
z.ai's coding endpoint does not serve. A third service, `responses-proxy`,
therefore sits between them and translates Responses to Chat Completions. It is
built from a pinned commit on a fork (see `e2e/responses-proxy/Dockerfile` for why
the published release is unusable here) and exists only for this test — it is not
part of the connector or any production path.

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
invocation. Native beta traces are preserved in the raw trace pipeline,
normalized by `coding_agent/claude`, and validated together on the host. Prompt
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
ceiling, and a ten-minute timeout. Claude Code's model tiers are mapped onto GLM
models: `E2E_CLAUDE_MODEL` sets both the main and Sonnet tiers, and
`E2E_CLAUDE_HAIKU_MODEL` (default `glm-4.5-air`) covers the Haiku tier used for
background requests.

```bash
E2E_CLAUDE_MODEL=glm-4.7 E2E_CLAUDE_HAIKU_MODEL=glm-4.5-air ./scripts/e2e-claude.sh
```
