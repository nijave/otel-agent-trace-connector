# Collector instances (HA) analysis

Option B now has runnable artifacts: the routing e2e
(`scripts/e2e-routing.sh`, `compose.e2e-routing.yaml`) proves the
consistent-hash affinity with the pinned collector versions, and
`examples/k8s-ha/` carries the Kubernetes shape with the k8s resolver and
RBAC. The analysis below stays as the rationale record.

This document records what it would take to run this Collector distribution as
N instances for high availability, and which issues block that today, as
analysis rather than an implementation plan: none of it exists as code yet.

Facts about upstream components reflect `loadbalancingexporter` documentation
and source at Collector v0.159.0, the version pinned in
`builder-config.yaml`.

## Current state

Every artifact in the repository assumes one instance per deployment:

- `collector-config.yaml`, `examples/otelcol-s3.yaml`, and the bundled image
  config expose OTLP on fixed ports and export to local files or S3.
- The e2e Compose stack runs a single `collector` service writing all output
  files into one shared volume path.
- No Kubernetes manifests exist in the repository.

The connector itself has three paths with different statefulness:

| Path | Edge | State |
| --- | --- | --- |
| Codex logs | logs-to-traces | In-memory per-instance map keyed on the `conversation.id` log attribute (`internal/codex/connector.go`) |
| Cursor logs | logs-to-traces | Same shape, keyed on `cursor.conversation.id`, dedupe on `cursor.event.id` (`internal/cursor/connector.go`) |
| Claude Code + GenAI semconv | traces-to-traces | Stateless copy-and-normalize; safe behind any load balancer today |

Stateful-edge facts that matter for HA:

- Turn and burst state never leaves the process. The connector defines no
  storage-extension hook. `file_storage` appears only in exporter sending
  queues and is deliberately local to each replica.
- Shutdown drains every active turn immediately with finish reason `shutdown`
  (`flushAll`). Any restart truncates all in-flight turns regardless of
  termination grace period.
- Trace IDs derive from SHA-256 of conversation ID plus prompt timestamp.
  Replaying the complete event set through one instance is idempotent. A
  partial event set anchors on its earliest observed event instead of the
  prompt, so a fragment can carry a different trace ID than the complete turn
  would have.

## Blocking issues for N replicas behind one network load balancer

1. **No per-conversation affinity at ingress.** A k8s Service or any
   connection-level balancer spreads requests across replicas without seeing
   `conversation.id`. Events of one turn land on different instances; each
   builds partial state and each emits its own incomplete trace.
2. **Fragment traces are silently wrong two ways.** When both replicas see the
   prompt, both derive the same trace ID and downstream sees duplicated roots
   with doubled usage rollups. When only one saw the prompt, the other's
   fragment derives a different trace ID and downstream sees two unrelated
   partial traces. Either way, canonical data corrupts without errors.
3. **The network layer cannot pin Cursor.** Cursor exports from its
   own cloud infrastructure over OTLP/HTTP with many source connections, so no
   ingress-side affinity trick keeps a conversation on one replica. Only
   content-aware routing fixes Cursor.
4. **Rolling restarts truncate active turns.** Drain-on-shutdown flushes all
   turns as `shutdown` immediately. This is existing single-instance behavior,
   but N replicas restart more often, so the frequency of truncated turns
   rises unless operators accept it or drain semantics change.
5. **HA does not change crash loss.** Active state dies with the process.
   design.md documents that limitation, and HA does not remove it; failover
   just relocates where the next events land.

## What does not block multi-instance

- The Claude/GenAI traces edge is stateless and already scales horizontally.
- Exporter sending queues are per-replica by design; each replica needs its own
  durable volume (already documented in the S3 reference).
- Self-metrics (`coding_agent.active_turns`, turns emitted/dropped/truncated)
  work per replica; dashboards need sums across replicas, not code changes.
- The health check extension runs fine per replica.

## Options considered

### A. Active/passive failover (no code change)

Run one writable replica; replace it on failure (k8s Deployment with
`replicas: 1`, `Recreate`, or a leader-elected writer). Availability window
equals restart time. Crash still loses active turns. Cheapest option and no
repository change beyond manifests that do not exist yet.

### B. Two-tier consistent-hash routing (upstream-supported scale-out)

Put a stateless gateway tier in front: stock `otelcol-contrib` (no custom
build needed) running an OTLP receiver and the `load_balancing` exporter with
attribute-based routing:

```yaml
exporters:
  load_balancing:
    routing_key: attributes
    routing_attributes:
      - conversation.id
      - cursor.conversation.id
    resolver:
      k8s:
        service: coding-agent-collector.telemetry.svc
```

At v0.159.0 the `load_balancing` exporter supports logs at beta stability and
`routing_key: attributes` checks resource, scope, and log-record attributes,
so both conversation attributes route correctly even though they never appear
on the same record (missing attributes encode deterministically). Consistent
hashing gives the same backend decision on every tier-1 replica sharing the
same resolved endpoint list, so tier-1 scales freely. Tier 2 runs this
distribution unchanged behind a headless service.

Required supporting changes:

- Add `loadbalancingexporter` to tier-1 images (stock contrib image avoids
  touching `builder-config.yaml`; the custom distribution stays tier-2-only).
- k8s resolver needs RBAC for `get/list/watch` on EndpointSlices, otherwise
  the endpoint list stays empty. DNS against a headless service is the slower
  fallback.
- Enable tier-1 queue/retry deliberately: the exporter-level resilience
  options default to off, so a temporarily unreachable tier-2 pod surfaces as
  receiver errors back to agents once sub-exporter retries exhaust.

Remaining caveats, documented upstream:

- Topology changes remap roughly R/N routes. An active turn whose events span
  the change splits across replicas and produces exactly the fragments
  described above. Logs have no `groupbytrace` analog, so nothing makes this
  atomic; fixed replica counts reduce it instead.
- The resolver does not health-check backends; dead-endpoint handling relies
  on retry plus EndpointSlice updates.

### C. Shared external state store

Move turn state behind Redis, SQL, or similar so any replica can advance any
conversation. Not built, and design.md defers persistent state until a
demonstrated need exists. It would require an explicit storage contract,
schema versioning, replay/deduplication policy, and latency and failure
handling inside the correlation hot path. Largest change; rejected under this
repository's simplicity rules until B proves insufficient.

### D. Partition ingestion upstream

Deliver agent telemetry through Kafka partitioned by conversation ID into a
Kafka receiver. Moves routing outside the Collector but introduces
infrastructure the repository does not use anywhere and a new receiver
dependency. Only worth revisiting if a broker already sits in the path.

## Recovery property worth keeping in mind

Because raw pipelines preserve source logs and traces independently, and
because trace IDs are deterministic given the complete event set, canonical
output can be rebuilt after any topology churn or incident by replaying raw
logs through a single instance. Downstream consumers dedupe on trace ID. This
works today and is the safety net that makes option B's remap caveat
acceptable.

## Recommendation

Option A if the goal is failover only; option B if the goal is horizontal
scale, using the stock contrib image for tier 1 and accepting the documented
remap caveat during topology changes. Option C stays deferred per the design
document. Nothing here requires connector code changes; the blocking work is
deployment shape, routing configuration, and operational acceptance of
truncated turns across restarts.
