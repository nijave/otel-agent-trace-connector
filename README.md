# Coding-agent OpenTelemetry connector

This repository provides an external OpenTelemetry Collector connector and an
OCB-built distribution for coding-agent traces:

- **Codex:** correlates structured `codex.*` OTLP logs into one canonical trace
  per user turn.
- **Claude Code:** preserves its native span hierarchy and normalizes the
  interaction, LLM, and tool span names and attributes into the same canonical
  vocabulary.

The canonical tree is:

```text
invoke_agent <agent>
├── chat <model>
└── execute_tool <tool>
```

The raw vendor logs and traces are exported separately. Normalization never
copies prompt text, tool arguments, or tool output into generated Codex spans.

Repository layout:

```text
.
├── connector/codingagentconnector/  # the connector component (own Go module)
│   ├── config.go, factory.go        #   public Collector component surface
│   ├── metadata.yaml, doc.go        #   mdatagen source and generate directive
│   ├── internal/codex/              #   stateful log correlation and trace building
│   └── internal/claude/             #   stateless native-span normalization
├── e2e/                             # real agent runners and OTLP JSON validator
├── examples/otelcol-s3.yaml         # S3 export with persistent local queues
├── builder-config.yaml              # pinned OCB distribution
├── go.work                          # ties the component and validator modules
├── compose.ca.yaml                  # optional managed/private CA overlay
├── compose.claude.aws-profile.yaml  # optional AWS profile/SSO overlay
└── collector-config.yaml            # raw and canonical pipelines
```

The connector lives in its own module under `connector/codingagentconnector/`,
matching the layout of components in `opentelemetry-collector-contrib` so it can
be upstreamed with minimal changes.

## Status

The component is at development stability. It uses Collector v0.156.0 and Go
1.25. Codex reconstruction state is in memory and does not survive Collector
restarts. See [the design document](docs/design.md) for decisions, assumptions,
and operational limits.

## Build

The repository is a Go workspace (`go.work`) with two modules: the connector
component under `connector/codingagentconnector/` and the E2E validator at the
repo root. Test each module:

```bash
go test ./...                                   # root module (E2E validator)
(cd connector/codingagentconnector && go test ./... && go test -race ./...)
```

If your normal Go module cache is read-only, use a writable cache:

```bash
GOMODCACHE=/tmp/otel-agent-trace-connector-gomodcache go test ./...
```

Build the custom Collector container:

```bash
docker build -t otelcol-coding-agents:dev .
```

Or install OCB v0.156.0 and generate the distribution directly. The OCB-generated
`dist/` module is not part of the workspace, so disable it for the build:

```bash
GOWORK=off builder --config builder-config.yaml
./dist/otelcol-coding-agents --config collector-config.yaml
```

Regenerate the component's mdatagen artifacts after editing `metadata.yaml`:

```bash
(cd connector/codingagentconnector && go generate ./...)
```

## Collector configuration

The connector can bridge either logs or traces into a traces pipeline:

```yaml
connectors:
  coding_agent:
    turn_timeout: 10m
    reorder_window: 30s
    max_active_turns: 10000
    max_events_per_turn: 1000
  coding_agent/claude:

service:
  pipelines:
    logs/vendor:
      receivers: [otlp]
      exporters: [raw_logs, coding_agent]
    traces/native:
      receivers: [otlp]
      exporters: [raw_traces, coding_agent/claude]
    traces/canonical:
      receivers: [coding_agent, coding_agent/claude]
      exporters: [canonical]
```

`reorder_window` starts after the most recently received Codex event. A turn
with at least one `response.completed` event is finalized when that window is
quiet. A turn without completion is finalized by `turn_timeout`, shutdown, a
new prompt in the same conversation, or bounded-state eviction.

Codex telemetry must be configured in the user-level `config.toml`; Codex
ignores project-local `[otel]` configuration. Prompt logging should remain off.
See the [official Codex observability documentation](https://developers.openai.com/codex/config-advanced#observability-and-telemetry).

Claude Code should export its native beta traces directly. See the
[official Claude Code monitoring documentation](https://code.claude.com/docs/en/monitoring-usage#traces-beta).

### S3 reference deployment

[`examples/otelcol-s3.yaml`](examples/otelcol-s3.yaml) is a production-oriented
reference that stores raw logs, raw native traces, and canonical traces under
separate S3 prefixes. Each S3 exporter uses a bounded `sending_queue` backed by
the Collector file-storage extension, so completed batches waiting for S3 can
survive a Collector restart.

Build the distribution, provide a pre-existing bucket, and run the example:

```bash
builder --config builder-config.yaml
export OTEL_S3_BUCKET=my-telemetry-bucket
export AWS_REGION=us-east-1
export OTEL_QUEUE_DIRECTORY=/var/lib/otelcol/sending-queue
./dist/otelcol-coding-agents --config examples/otelcol-s3.yaml
```

Mount `OTEL_QUEUE_DIRECTORY` on durable local storage with sufficient capacity;
an ephemeral container filesystem defeats restart recovery. Each Collector
replica should have its own local volume. The default file-storage limit is 1
GiB per queue database and can be changed with
`OTEL_QUEUE_MAX_SIZE_BYTES`. Queue capacity is also bounded to 10,000 requests
per exporter.

The example intentionally contains no access keys. Supply credentials through
the standard AWS SDK credential chain, preferably with a workload, task, or
instance role granting only the required bucket access. `OTEL_S3_BASE_PREFIX`
defaults to `coding-agent-otel` and can be overridden.

The queues persist telemetry that has reached an exporter; they do not persist
the connector's active Codex turn-correlation state. Also account for the raw
telemetry privacy considerations below before enabling this configuration.

## Tests

The normal test suite includes:

- event parsing and type-coercion unit tests;
- configuration validation tests;
- deterministic trace construction, hierarchy, token, status, and redaction tests;
- bounded-state, out-of-order, timeout/shutdown, and turn-splitting tests;
- Collector factory/lifecycle integration tests;
- Claude native-tree normalization and non-mutation tests;
- the E2E output validator as a separately compiled Go command.

The live E2E is intentionally separate because it calls a real model and may
incur API cost.

## Live Codex E2E

The Compose test builds the custom Collector, launches a real non-interactive
Codex session which must use one harmless shell tool, waits for trace
reconstruction, and validates the exported OTLP JSON. It requires a unique run
ID and checks the root/child hierarchy, canonical attributes, completion state,
and absence of sensitive copied fields.

Prerequisites:

- Docker with Compose v2;
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

The E2E defaults to `gpt-5.1-codex-mini`, the smaller, lower-cost Codex model.
Its image installs the standard Debian `ca-certificates` package, so public TLS
roots work without depending on host paths. Environments with TLS interception
or private certificate authorities can add their PEM bundle without replacing
the image's public roots:

```bash
E2E_CA_BUNDLE=/absolute/path/to/managed-ca-bundle.pem ./scripts/e2e.sh
```

TLS verification remains enabled. The runner authenticates through Codex's
noninteractive API-key login; its credential store exists only inside the
ephemeral runner container.

The script writes raw logs, raw native traces, and canonical traces under
`.e2e-output/`. To inspect or rerun Compose manually:

```bash
export E2E_RUN_ID="manual-$(date +%s)"
docker compose build
docker compose up --detach --wait collector
docker compose run --rm --no-deps codex
docker compose run --rm --no-deps validator
docker compose down
```

When invoking Compose directly with a managed CA, add
`-f compose.yaml -f compose.ca.yaml` to each command and export
`E2E_CA_BUNDLE` first. The wrapper script selects that overlay automatically.

The repository's automated verification does not run this paid test.

## Live Claude Code E2E

The Claude Compose test runs pinned Claude Code exclusively through Amazon
Bedrock in recommended bare, non-interactive mode and requires exactly one Bash
tool invocation. Native beta traces are preserved in the raw trace pipeline,
normalized by `coding_agent/claude`, and checked together by the validator.
Prompt text, tool arguments, tool output, and raw API bodies remain disabled.
`ANTHROPIC_API_KEY` and the direct Anthropic API are not used.
The Bedrock-backed live test is prepared but has not been run.

Before running, submit the Anthropic model use-case form once in the Bedrock
model catalog, ensure the model or inference profile is available in the chosen
region, and grant the test principal these actions:

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

Restrict `Resource` to approved foundation-model and inference-profile ARNs
where IAM permits. Initial model subscription can additionally require the AWS
Marketplace permissions documented in the official Claude Code Bedrock guide.

### Recommended: short-lived Bedrock bearer token

The recommended wrapper uses AWS's official token-generator library on the
host. It resolves your normal host AWS credential chain, generates a
region-bound Bedrock bearer token with a 15-minute default lifetime, and passes
only that token to the agent container. AWS profiles, credential processes, SSO
state, and role assumption never need to work inside the container.

Install `uv`, authenticate the AWS CLI on the host, then run:

```bash
aws sso login --profile my-bedrock-profile
AWS_PROFILE=my-bedrock-profile AWS_REGION=us-east-1 \
  ./scripts/e2e-claude-bedrock.sh
```

Set `E2E_BEDROCK_TOKEN_TTL_SECONDS` between 900 and 43200 to request another
lifetime. The actual key expires at the earlier of that duration or the source
AWS session. The wrapper does not print or write the token; it passes the token
as an environment variable to an ephemeral container. Treat local Docker access
as privileged because Docker operators can inspect container environments.

### Other credential-chain options

Temporary AWS credentials can be supplied directly to the base runner:

```bash
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=...
./scripts/e2e-claude.sh
```

For a profile without generating a bearer token, authenticate on the host and
run the base wrapper. It mounts `~/.aws` read-only, including the SSO cache:

```bash
aws sso login --profile my-bedrock-profile
AWS_PROFILE=my-bedrock-profile AWS_REGION=us-east-1 ./scripts/e2e-claude.sh
```

Set `E2E_AWS_CONFIG_DIR` if the configuration directory is not `~/.aws`.
An already-generated Bedrock key can be supplied as
`AWS_BEARER_TOKEN_BEDROCK`.

The test defaults to the US cross-region Claude Haiku 4.5 inference profile,
at most three agent turns, a $0.25 budget ceiling, and a ten-minute timeout.
Override the model with another inference profile ID or application inference
profile ARN that the account can invoke:

```bash
AWS_REGION=us-east-1 \
  E2E_CLAUDE_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
  ./scripts/e2e-claude-bedrock.sh
```

For a managed/private CA, set `E2E_CA_BUNDLE` just as for the Codex test. The
Claude-specific Compose overlay appends it to the image's installed public
roots without disabling certificate validation.

## CI and releases

GitHub Actions runs formatting, shell syntax, lint, vet, unit/integration tests,
race tests, custom Collector builds, Collector/Compose validation, container
builds, and GoReleaser configuration checks. Live agent E2Es remain manual and
never receive credentials in CI.

Pushing a semantic version tag such as `v0.1.0` runs GoReleaser and creates a
GitHub release containing cross-platform custom Collector archives and SHA-256
checksums:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

## References

- [OpenTelemetry Collector Builder](https://opentelemetry.io/docs/collector/extend/ocb/)
- [OpenTelemetry Collector connectors](https://opentelemetry.io/docs/collector/components/connector/)
- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
- [Codex observability and telemetry](https://developers.openai.com/codex/config-advanced#observability-and-telemetry)
- [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)
- [Claude Code on Amazon Bedrock](https://code.claude.com/docs/en/amazon-bedrock)
- [Amazon Bedrock API keys](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html)
- [AWS S3 exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.156.0/exporter/awss3exporter)
- [File storage extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.156.0/extension/storage/filestorage)
