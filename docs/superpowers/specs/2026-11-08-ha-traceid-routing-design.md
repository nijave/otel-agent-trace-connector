# HA routing by statefulness (2026-11-08)

**Goal:** Spread stateless telemetry evenly across tier-2 Collectors in the
two-tier HA example, while Codex and Cursor turn records keep their
per-conversation affinity.

**Status:** design approved in chat on 2026-11-08.

## Problem

`examples/k8s-ha/tier1-config.yaml` sends logs and traces through one
`load_balancing` exporter with
`routing_key: attributes` and
`routing_attributes: [conversation.id, cursor.conversation.id]`. Missing
attributes encode deterministically, so every record without either key
hashes to one value and lands on one tier-2 pod. A live deployment showed the
scale of this (2026-11-07, 7 days):

- Claude Code: 0 of about 4,100 native spans and logs carry either key.
- Pi: raw spans carry `sessionId`, not `conversation.id`.
- Codex: 1,017 of 1,599 log records (records outside a turn) carry neither.

This traffic shares one pod. The stateless traces edges cannot scale
out, and that pod's restart truncates the most work. `multi-instance-ha.md`
says the Claude/GenAI edge "already scales horizontally", which the example
config contradicts.

## Design

Route by statefulness in tier 1:

| Traffic | Exporter | Reason |
| --- | --- | --- |
| Log records with `conversation.id` or `cursor.conversation.id` | `load_balancing/conversation`, `routing_key: attributes` (unchanged keys) | Codex and Cursor correlation needs one turn's records on one instance |
| All other log records | `load_balancing/trace`, `routing_key: traceID` | Stateless edges; spreads load |
| All traces | `load_balancing/trace`, `routing_key: traceID` | Every traces edge is stateless; keeps a trace's spans together |

A `routing` connector in tier 1 selects the logs exporter per log record.
The condition must read log-record attributes, not only resource
attributes, because Codex sets `conversation.id` on the record.

### Facts to verify before building (Collector v0.163.0)

1. `load_balancing` accepts `routing_key: traceID` for logs. If it does not,
   use the closest supported key that spreads by agent process (for example
   `resource`) and record the choice and the reason.
2. The `routing` connector at this version can route on log-record context
   and splits one batch across routes record by record. If it routes only
   whole resource groups, record what that means for a Codex batch that
   mixes records with and without `conversation.id`. A mixed group must
   still reach the conversation exporter.
3. A record that has `conversation.id` must never go to the traceID route.

Cite the upstream source file or README for each answer in the
implementation notes.

## Scope

- `examples/k8s-ha/tier1-config.yaml` and `examples/k8s-ha/README.md`.
- `compose.e2e-routing.yaml`, `e2e/routing/`, `scripts/e2e-routing.sh`, and
  the routing validator: replay, with no API cost, a Claude Code native-traces
  fixture with many distinct trace IDs (reuse
  `internal/claude/testdata/claude-native-traces.json`, rewriting trace IDs
  per copy) next to the existing Codex replay.
- Assertions: Codex affinity holds as today (each conversation's canonical
  turn comes from exactly one backend); Claude canonical traces arrive at
  **both** backends; each trace's spans reach exactly one backend.
- `docs/multi-instance-ha.md`: replace the single-exporter Option B config
  with the split design, and correct the "scales horizontally" statement so it
  is true of the example.
- The builder and the connector code do not change.

## Out of scope

Persistent turn state, topology-change atomicity (the documented R/N remap
caveat stays), and tier-2 config changes.
