# Two-tier HA Kubernetes example

This example deploys the two-tier consistent-hash routing topology from
`docs/multi-instance-ha.md` (Option B) on Kubernetes. Agents send OTLP to the
`coding-agent-ingest` Service, which reaches the tier-1 gateway. The gateway
routes by statefulness: a `routing` connector sends log records that carry
`conversation.id` or `cursor.conversation.id` to a `load_balancing` exporter
hashing those attributes, so Codex and Cursor turn records stay on one
tier-2 replica behind the headless `coding-agent-collector` Service, while
every other log record and all traces go to a second exporter routing by
trace ID, spreading stateless load across the replicas without splitting a
trace. Each tier-2 replica runs this repository's distribution and
normalizes vendor logs and traces into canonical output.

`compose.e2e-routing.yaml` runs this same topology locally with Docker
Compose and a Go affinity checker; run it before you deploy here if you want
to see the routing guarantee hold end to end. `docs/multi-instance-ha.md`
covers the full analysis behind this topology.

## Prerequisites

- Pull `ghcr.io/nijave/otel-agent-trace-connector:v0.4.2`, the image the
  repo's release workflow builds and pushes on each `v*` tag; `tier2.yaml`
  already points at it. A fork can instead build the root `Dockerfile` and
  point the `image:` field in `tier2.yaml` at its own registry.
- Point `CANONICAL_OTLP_ENDPOINT` in `tier2.yaml` at a trace backend that
  accepts OTLP gRPC.

## Apply order

```
kubectl apply -f tier2-config.yaml
kubectl apply -f tier2.yaml
kubectl apply -f tier1-config.yaml
kubectl apply -f tier1.yaml
```

Tier 2 needs to exist before the gateway's k8s resolver can discover it, so
apply the tier-2 files first. `tier1.yaml` also creates the ServiceAccount,
Role, and RoleBinding the gateway needs to list, get, and watch
EndpointSlices for the headless service; without that Role the resolver's
endpoint list stays empty and the gateway routes nothing.

## Operational caveats

These carry over from `docs/multi-instance-ha.md` ("Required supporting
changes", "Remaining caveats", "Recovery property"):

- A topology change (scaling tier 2, a rolling update) remaps roughly R/N
  routes, where R is the route count and N the replica count. Any turn whose
  events span the change splits across the old and new replica and produces
  the fragment traces the spec describes.
- A rolling restart of a tier-2 replica drains its active turns immediately;
  the `otelcol_coding_agent_turns_emitted` self-metric records these with
  `finish_reason=shutdown`. This is a metric label, not a canonical span
  attribute.
- This minimal example exports canonical traces only; it has no raw log or
  raw trace exporter and cannot replay source telemetry by itself. Raw replay
  requires adding a separate raw-preservation branch before the connector.
  `examples/otelcol-s3.yaml` shows that pattern. With raw telemetry preserved,
  replaying the complete event set through one instance rebuilds deterministic
  canonical IDs, and downstream consumers can dedupe on trace ID.
