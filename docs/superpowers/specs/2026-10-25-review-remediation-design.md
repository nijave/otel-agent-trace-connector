# Review remediation design (2026-10-25)

**Goal:** Correct every functional and documentation issue found in the
2026-10-25 documentation/code review while preserving the connector's
accuracy, privacy, bounded-state, and closed-vocabulary contracts.

**Status:** design approved in chat on 2026-10-25. This spec is the record for
the implementation plan that follows.

## Background and success criteria

The review found three implementation defect classes and several places where
the documentation describes behavior the code no longer has:

1. Codex native chats inherit one model selected across an entire OTLP batch,
   so a batch containing several traces can assign the wrong model.
2. Codex-native and Pi normalizers copy span events without applying the
   canonical event policy.
3. Codex and Cursor keep deduplication keys for every overflow event, so
   `max_events_per_turn` bounds retained events but not per-turn memory.
4. GenAI spans without `gen_ai.operation.name` reach canonical output without
   the three attributes the canonical contract requires on every span.
5. The component README, HA documentation/example, and GenAI design/matrix
   text contain stale or mutually contradictory behavior descriptions.

The remediation is complete when:

- model attribution is isolated by trace ID;
- every span emitted by the Codex-native and Pi edges has canonical-only
  attributes on both the span and any surviving events;
- per-turn event storage and dedupe storage are both bounded by
  `max_events_per_turn`, while overflow redeliveries cannot change turn
  finalization state;
- every canonical span carries `gen_ai.operation.name`,
  `coding_agent.source`, and `coding_agent.client.name`;
- the reviewed documentation matches implementation and deployment behavior;
- focused regression tests fail before their fixes and pass afterward, and
  `./scripts/check.sh` passes.

## Global constraints

- Keep the canonical attribute vocabulary unchanged. These fixes require no
  new canonical keys and no `metadata.yaml` changes.
- Preserve raw-pipeline fidelity. Filtering applies only to canonical output;
  normalizers continue to advertise `MutatesData: false` and must not mutate
  their input batches.
- Keep the traces edges stateless. Do not add cross-batch model state,
  persistent dedupe state, or a storage extension.
- Prefer removal and existing mechanisms over new abstractions. Reuse
  `internal/content.Strip`; do not introduce a second event policy or a
  bounded-LRU subsystem.
- Preserve existing span IDs, trace IDs, timestamps, kinds, status, links, and
  hierarchy except where the current edge already deliberately changes them.
- Each functional change lands with focused regression tests and the minimum
  documentation update needed to keep the contract accurate.

## Design

### 1. Scope Codex native model lookup by trace

`internal/codextraces/normalizer.go` currently calls `batchTurnModel(input)`
once and passes the resulting string to every response span. Replace that
single string with a batch map keyed by `pcommon.TraceID`.

For each claimed resource group and each turn span
(`session_task.turn`, `.compact`, or `.review`) carrying a non-empty string
`model`, record the model under that turn span's trace ID. If several turn
spans in one trace carry models, retain the model from the earliest turn by
start timestamp, matching the existing earliest-root reparenting rule. When
normalizing a response span, look up `models[span.TraceID()]`:

- a matching model produces `chat <model>` and `gen_ai.request.model`;
- no turn for that trace leaves the span as bare `chat` with no request-model
  attribute;
- turns and responses from other traces in the same batch have no effect.

No new state survives `ConsumeTraces`. Response-only batches retain their
current behavior because the connector cannot recover a model that is absent
from the batch.

Tests:

- one batch with two traces and different turn models emits one correctly
  named and attributed chat per trace;
- models do not leak across resource groups or trace IDs;
- the earliest turn supplies the model if one trace contains several turn
  spans;
- the existing response-without-turn test continues to emit bare `chat`.

Update `docs/harnesses/codex.md` only where needed to state explicitly that
model inheritance is per trace, not batch-wide.

### 2. Apply the canonical event policy on Codex-native and Pi spans

After each recognized Codex-native span has been normalized, run
`content.Strip(span)`. After each recognized Pi span has been normalized and
before orphan reparenting, run `content.Strip(span)`. Foreign spans still drop
under each edge's existing claiming rules.

This gives both edges the same behavior as Claude and GenAI:

- span attributes remain within the canonical allowlist;
- events named in `contentEventNames` are removed whole;
- other events survive with only canonical attributes;
- Codex built-in tool-result events continue to be removed whole by the
  existing tool mapping because their data has already been mapped onto the
  tool span;
- the copied input batch remains unchanged.

The fix deliberately preserves safe operational events instead of dropping
all events. The canonical policy already defines which events and attributes
may survive; duplicating that policy in provider packages would create drift.

Tests for each edge:

- a kept span carrying a known content event loses the event;
- a kept span carrying an operational event with a non-canonical sensitive
  attribute keeps the event name/timestamp but loses that attribute;
- a surviving event with a canonical exception attribute retains it;
- raw input still contains the original event and attributes after consume;
- existing canonical fixtures continue to pass cross-harness conformance.

Update the Codex and Pi harness matrices to describe event handling as part of
the mapping rather than implying that rebuilding the span attribute map alone
covers events.

### 3. Bound dedupe state after truncation

For Codex `turnState.add` and Cursor `burstState.add`, retain the current
behavior while `len(events) < maxEvents`: append the event and remember its
fingerprint/event ID. Once the event slice is full:

- set `truncated = true`;
- do not append the event;
- do not add its fingerprint/event ID to `seen`;
- do not change `first`, `last`, `lastSeen`, prompt completion state, or any
  other finalization input.

Consequently, `seen` contains at most one entry per retained event and is
bounded by `maxEvents`, like `events`. Duplicate retained events still dedupe
exactly. An overflow redelivery may re-enter `add`, but it has no effect and
cannot defer quiet-window or timeout finalization. The dedupe metric counts
only redeliveries actually recognized by the retained dedupe set; it does not
claim to count every record discarded by truncation.

This chooses a hard memory bound over unbounded exactly remembered overflow
history. An LRU is unnecessary: overflow events already have no state-changing
path when the turn is full, so remembering their IDs buys no finalization or
output correctness.

Tests for both correlators:

- with `maxEvents = 1`, many unique overflow events leave both `events` and
  `seen` at length one;
- duplicates of a retained event still dedupe and increment the existing
  dropped-event metric;
- repeated overflow redeliveries do not change `lastSeen`, `first`, `last`,
  `completeSeen`, or `completionAt`;
- existing redelivery-storm tests still finalize with the same reason;
- a post-truncation completion remains absent from output and cannot turn a
  timeout into a completion;
- a duplicate of an event retained before truncation remains deduped after
  truncation.

Update `docs/design.md` and the component README delivery/bounds text to say
that dedupe covers the retained event set; overflow IDs are deliberately not
remembered after truncation. The generated self-metric description remains
accurate and needs no regeneration.

### 4. Drop GenAI spans that cannot satisfy the canonical contract

In `internal/genai/normalizer.go`, a span on a matched GenAI scope must carry
`gen_ai.operation.name` to enter canonical output. Filter spans lacking that
key before normalization, then remove empty scope-span and resource-span
groups. Do not synthesize an operation from a wire span name: an arbitrary
name is not evidence of a GenAI operation, and inventing one would misclassify
application spans.

Spans with the key retain current normalization. Dropping an unclassified
span follows the same fail-closed rule already used for unmatched scopes and
unknown vendor attributes. Existing parent-link behavior is unchanged; this
edge already drops unmatched scopes without synthesizing replacement parents.

Tests:

- a group containing only a span without the operation key emits nothing;
- a mixed group emits only operation-bearing spans, with no empty output
  resource or scope groups;
- operation-bearing spans retain all current mappings and required keys;
- cross-harness conformance continues to enforce required keys on every
  emitted span.

Update `docs/harnesses/genai-scopes.md` to say these spans drop, and update
`docs/design.md` so it no longer says unmatched application scopes pass
through or that the whole claimed resource group is copied. The canonical
required-key statement remains unchanged and becomes true for this path.

### 5. Repair the component README

Bring `connector/codingagentconnector/README.md` into agreement with
`docs/canonical-attributes.md`, `docs/harnesses/`, and the implementation:

- remove `coding_agent.turn.finish_reason`, `coding_agent.turn.complete`,
  `coding_agent.turn.prompt_observed`,
  `coding_agent.turn.events_truncated`, `coding_agent.model_provider`,
  `coding_agent.cursor.*`, `coding_agent.cursor.billable`, and
  `coding_agent.tool.success` from emitted-attribute tables;
- state that usage lives on chat spans for the logs edges and that native
  edges retain only source-backed canonical usage;
- state that Codex `provider_name` drops and that close reasons and truncation
  are exposed through self-metrics, with timeouts also setting root Error
  status;
- state that Codex tool failure maps to OTel span status, not a vendor success
  attribute;
- replace Copilot vendor pass-through claims with the actual mappings:
  `github.copilot.cost` maps to `coding_agent.usage.cost` on chat spans, and
  other vendor keys drop;
- document `capture_identity` and `signals`, including their defaults and the
  fact that identity defaults on;
- describe `max_events_per_turn` as a bound on retained events and dedupe
  state, with truncation reported by the self-metric rather than a span
  attribute;
- keep the example configuration valid and consistent with the root README.

No new behavior is introduced by this task. Any statement that cannot be
derived from the current canonical vocabulary and normalizers is removed
rather than preserved for compatibility.

### 6. Correct HA recovery and current-state documentation

The minimal Kubernetes example has no raw exporters. Do not invent a raw
backend or add deployment machinery to this bug-fix effort. Instead:

- `examples/k8s-ha/README.md` must state that this example exports canonical
  traces only and therefore cannot, by itself, replay raw telemetry;
- recovery text must say raw replay is available only when operators add a
  separate raw-preservation branch, with `examples/otelcol-s3.yaml` as the
  reference pattern;
- shutdown wording must identify `finish_reason=shutdown` as the
  `otelcol_coding_agent_turns_emitted` metric label, not a canonical span
  attribute;
- `docs/multi-instance-ha.md` current-state text must acknowledge the
  existing Compose routing stack and `examples/k8s-ha/` manifests instead of
  saying no multi-instance or Kubernetes artifacts exist;
- the general recovery section must make its raw-preservation precondition
  explicit rather than assuming every deployment has raw pipelines.

The routing architecture itself is unchanged.

### 7. Verification and live coverage

Each task follows test-driven development: add the focused failing test,
confirm its failure for the stated reason, implement the minimal change, and
rerun the package tests. After all tasks:

```bash
GOTOOLCHAIN=auto ./scripts/check.sh
```

must pass. Because shared normalizers and canonical behavior change, the
repository instruction requires every affected live harness stack before a
PR: Codex, Claude Code, OpenAI-adhoc, Strands, OpenCode, Pi, Copilot, and
OpenHands. Cursor remains fixture-only. These paid runs are execution-time
verification, not part of the documentation-only design commit.

The routing e2e is credential-free and should also run when HA documentation
changes are accompanied by any deployment configuration change; this plan
changes no routing configuration, so it is not required for the doc-only HA
correction.

## Out of scope

- Adding cross-batch or persistent model attribution.
- Remembering every overflow delivery exactly while also bounding memory.
- Introducing an LRU, persistent dedupe store, new configuration knob, or new
  canonical attribute.
- Reworking unrelated normalizer hierarchy or claiming rules.
- Adding raw exporters or a new backend to the minimal Kubernetes example.
- Relaxing canonical privacy or required-key guarantees to match stale docs.
- Running paid live e2es as part of writing the spec or implementation plan.

## Landing order

Land independent, reviewable commits in this order:

1. Codex per-trace model lookup with regression tests and matrix wording.
2. Codex-native and Pi event filtering with privacy regression tests and
   matrix wording.
3. Codex and Cursor bounded dedupe state with state-machine tests and bounds
   documentation.
4. GenAI operation-required filtering with tests and GenAI design/matrix
   corrections.
5. Component README correction.
6. HA documentation correction.

The first four are independent functional fixes and may be reordered if
implementation review requires it. Documentation-only commits 5 and 6 must
not weaken or contradict the behavior established by the earlier fixes.
