# Collector instances (HA) analysis

Option B now has runnable artifacts: the routing e2e
(`scripts/e2e-routing.sh`, `compose.e2e-routing.yaml`) proves the
consistent-hash affinity with the pinned collector versions, and
`examples/k8s-ha/` carries the Kubernetes shape with the k8s resolver and
RBAC. The analysis below stays as the rationale record.

This document records what it would take to run this Collector distribution as
N instances for high availability, and which issues block that today, as
analysis rather than an implementation plan.

Facts about upstream components reflect `loadbalancingexporter` documentation
and source at Collector v0.163.0, the version pinned in
`builder-config.yaml`.

## Current state

The bundled `collector-config.yaml` and `examples/otelcol-s3.yaml` run one
Collector per deployment. Multi-instance artifacts now exist in two forms:
`compose.e2e-routing.yaml` proves the two-tier consistent-hash topology
locally, and `examples/k8s-ha/` provides the Kubernetes tier-1 gateway and
tier-2 Collector manifests with the k8s resolver and RBAC. The Kubernetes
example exports canonical traces only; raw preservation is a separate
configuration choice shown by `examples/otelcol-s3.yaml`.

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

- The Claude/GenAI traces edge is stateless. With the tier-1 traceID route it
  spreads evenly across replicas, so it scales horizontally without any
  connector change (the pre-2026-11-08 example pinned everything to one
  attribute-hashed exporter, which did not).
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
build needed) running an OTLP receiver, a `routing` connector that splits
logs by statefulness, and two `load_balancing` exporters — one per routing
key:

```yaml
exporters:
  load_balancing/conversation:
    routing_key: attributes
    routing_attributes:
      - conversation.id
      - cursor.conversation.id
    protocol:
      otlp:
        tls:
          insecure: true
    resolver:
      k8s:
        service: coding-agent-collector.telemetry.svc
  load_balancing/trace:
    routing_key: traceID
    protocol:
      otlp:
        tls:
          insecure: true
    resolver:
      k8s:
        service: coding-agent-collector.telemetry.svc

connectors:
  routing:
    default_pipelines: [logs/trace]
    table:
      - condition: log.attributes["conversation.id"] != nil
                  or log.attributes["cursor.conversation.id"] != nil
        pipelines: [logs/conversation]

service:
  pipelines:
    logs/in:
      receivers: [otlp]
      exporters: [routing]
    logs/conversation:
      receivers: [routing]
      exporters: [load_balancing/conversation]
    logs/trace:
      receivers: [routing]
      exporters: [load_balancing/trace]
    traces:
      receivers: [otlp]
      exporters: [load_balancing/trace]
```

The route by statefulness: log records carrying `conversation.id` or
`cursor.conversation.id` go to the `attributes` exporter so Codex and Cursor
correlation keeps one turn's records on one instance; every other log record
and all traces go to the `traceID` exporter, which spreads them across tier 2
while keeping each trace's spans together. The connector conditions read
log-record attributes because Codex sets `conversation.id` on the record, not
the resource. The routing connector's default `action: move` removes matched
records from later evaluation and `default_pipelines` receive only the
leftovers, so a record that has a conversation attribute can never reach the
traceID route.

At v0.163.0 (verified against
`exporter/loadbalancingexporter/log_exporter.go` and
`connector/routingconnector/logs.go` in the collector contrib source):
`load_balancing` accepts `routing_key: traceID` for logs and splits a batch
record by record, giving records without a trace ID a random key so they do
not hot-spot one backend; the `routing` connector routes logs on the log-record
context and splits one incoming batch across routes record by record.
Consistent hashing gives the same backend decision on every tier-1 replica
sharing the same resolved endpoint list, so tier-1 scales freely. Tier 2 runs
this distribution unchanged behind a headless service.

Metrics through tier 1 need `routing_key: streamID` on a separate
`load_balancing/metrics` exporter once tier 2 runs `deltatocumulative` (Task
8 of the 2026-11-08 telemetry gaps plan): delta-to-cumulative conversion keeps
state per series, so a stream's points must all reach the same tier-2
replica. `streamID` routes on metric stream identity, which is exactly that
grouping.

Required supporting changes:

- Add `loadbalancingexporter` and `routingconnector` to tier-1 images (stock
  contrib image avoids touching `builder-config.yaml`; the custom
  distribution stays tier-2-only).
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

When a deployment configures raw-preservation branches, source logs and traces
remain available independently of canonical output. Because trace IDs are
deterministic given the complete event set, canonical output can then be
rebuilt after topology churn or an incident by replaying raw logs through a
single instance. Downstream consumers dedupe on trace ID. The S3 example shows
the required raw branches; the minimal Kubernetes HA example does not include
them, so this recovery path is unavailable there unless the operator adds raw
exporters and storage.

## Recommendation

Option A if the goal is failover only; option B if the goal is horizontal
scale, using the stock contrib image for tier 1 and accepting the documented
remap caveat during topology changes. Option C stays deferred per the design
document. Nothing here requires connector code changes; the blocking work is
deployment shape, routing configuration, and operational acceptance of
truncated turns across restarts.
