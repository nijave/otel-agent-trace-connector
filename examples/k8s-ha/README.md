# Two-tier HA Kubernetes example

This example deploys the two-tier consistent-hash routing topology from
`docs/multi-instance-ha.md` (Option B) on Kubernetes. Agents send OTLP to the
`coding-agent-ingest` Service, which reaches the tier-1 gateway. The
gateway's `load_balancing` exporter hashes each record's conversation
attribute and sends it to the matching tier-2 replica behind the headless
`coding-agent-collector` Service. Each tier-2 replica runs this repository's
distribution and normalizes vendor logs and traces into canonical output.

`compose.e2e-routing.yaml` runs this same topology locally with Docker
Compose and a Go affinity checker; run it before you deploy here if you want
to see the routing guarantee hold end to end. `docs/multi-instance-ha.md`
covers the full analysis behind this topology.

## Prerequisites

- Build and push the root `Dockerfile` to a registry the cluster can pull
  from, then point the `image:` field in `tier2.yaml` at it:
  ```
  docker build --tag <registry>/otelcol-coding-agents:<tag> .
  ```
  This repository does not publish a container image.
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
- A rolling restart of a tier-2 replica truncates its active turns
  immediately; each ends with `finish_reason=shutdown` instead of its normal
  completion reason.
- Recovery: the raw pipelines keep source logs and traces independently of
  the canonical pipeline, and trace IDs derive deterministically from the
  complete event set, so you can rebuild canonical output after any topology
  churn by replaying the raw logs through a single instance. Downstream
  consumers dedupe on trace ID.
