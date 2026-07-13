# End-to-end tests

The live e2e tests build the custom Collector, run a real coding agent in a
container, and validate the exported OTLP traces on the host with
`go test -tags=e2e ./e2e/validator`. They call real models and incur API cost, so
they are opt-in and never run in CI.

Both stacks share `compose.base.yaml` (the collector); each defines only its own
`agent` service. Output is written under `.e2e-output/`.

## Live Codex E2E

The Compose stack builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and validates the exported OTLP JSON. It checks the root/child
hierarchy, canonical attributes, completion state, and absence of sensitive
copied fields.

Prerequisites:

- Docker with Compose v2;
- a Go toolchain (validation runs as `go test` on the host);
- an `OPENAI_API_KEY` authorized for Codex/API usage;
- network access to build images and call the model.

Run it only when you intend to incur the model request:

```bash
export OPENAI_API_KEY=...
./scripts/e2e.sh
```

Optional overrides:

```bash
CODEX_VERSION=0.144.1 E2E_CODEX_MODEL=gpt-5.1-codex-mini E2E_AGENT_TIMEOUT=10m ./scripts/e2e.sh
```

The E2E defaults to `gpt-5.1-codex-mini`, the smaller, lower-cost Codex model. Its
image installs the Debian `ca-certificates` package, so public TLS works out of the
box. The runner authenticates through Codex's noninteractive API-key login; its
credential store exists only inside the ephemeral runner container.

To inspect or drive the stack manually (keep the collector up so its file exporter
can flush while the validation polls):

```bash
export E2E_RUN_ID="manual-$(date +%s)"
export OPENAI_API_KEY=...
docker compose up --detach --wait collector
docker compose run --rm --no-deps agent
TRACE_FILE="$PWD/.e2e-output/canonical-traces.json" go test -tags=e2e ./e2e/validator/
docker compose down
```

## Live Claude Code E2E

The Claude stack runs pinned Claude Code exclusively through Amazon Bedrock in
bare, non-interactive mode and requires exactly one Bash tool invocation. Native
beta traces are preserved in the raw trace pipeline, normalized by
`coding_agent/claude`, and validated together on the host. Prompt text, tool
arguments, tool output, and raw API bodies remain disabled. `ANTHROPIC_API_KEY`
and the direct Anthropic API are not used. The Bedrock-backed live test is
prepared but has not been run.

Before running, submit the Anthropic model use-case form once in the Bedrock model
catalog, ensure the model or inference profile is available in the chosen region,
and grant the principal that mints the token these actions:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "bedrock:InvokeModel",
      "bedrock:InvokeModelWithResponseStream",
      "bedrock:ListInferenceProfiles",
      "bedrock:GetInferenceProfile",
      "bedrock:CallWithBearerToken"
    ],
    "Resource": "*"
  }]
}
```

Restrict `Resource` to approved foundation-model and inference-profile ARNs where
IAM permits. Initial model subscription can additionally require the AWS
Marketplace permissions documented in the official Claude Code Bedrock guide.

### Credentials: one ephemeral Bedrock API key

The container receives exactly one credential — an ephemeral Bedrock API key
(`AWS_BEARER_TOKEN_BEDROCK`). All host AWS credential resolution (profiles, SSO,
credential processes, role assumption) stays on the host, outside the container.
See the [Bedrock API keys guide](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html).

`scripts/e2e-claude-bedrock.sh` mints a short-lived, region-bound token from your
normal host AWS credential chain (using AWS's official token generator; requires
`uv`) and passes only that token to the container:

```bash
aws sso login --profile my-bedrock-profile
AWS_PROFILE=my-bedrock-profile AWS_REGION=us-east-1 ./scripts/e2e-claude-bedrock.sh
```

Set `E2E_BEDROCK_TOKEN_TTL_SECONDS` between 900 and 43200 to request another
lifetime; the key expires at the earlier of that duration or the source AWS
session. The wrapper never prints or writes the token. Treat local Docker access
as privileged, since Docker operators can inspect container environments.

If you already have a Bedrock API key, run the test directly:

```bash
AWS_REGION=us-east-1 AWS_BEARER_TOKEN_BEDROCK=bedrock-api-key-... ./scripts/e2e-claude.sh
```

The test defaults to the US cross-region Claude Haiku 4.5 inference profile,
at most three agent turns, a $0.25 budget ceiling, and a ten-minute timeout.
Override the model with another inference profile ID or application inference
profile ARN that the account can invoke:

```bash
AWS_REGION=us-east-1 \
  E2E_CLAUDE_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
  ./scripts/e2e-claude-bedrock.sh
```
