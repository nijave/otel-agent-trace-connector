# Canonical identity attributes design

**Goal:** Let the connector carry a coding agent's user, team, machine, and
terminal context into canonical output under new keys, controlled by a connector
setting for the identity fields that count as PII.

**Status:** design approved in chat 2026-08-29; expanded during spec review the
same day with codex identity, hostname, and terminal type. This spec is the
record for the implementation plan that follows.

## Background

The canonical output today carries no user identity. Each harness normalizer
maps only a fixed set of keys, and two closed allowlists strip everything else:
`canonicalAttributeKeys` for span and event attributes
(`internal/canonical/vocabulary.go`) and `canonicalResourceKeys` for resource
attributes (`internal/canonical/resource.go`). Neither list holds a `user.*`,
`enduser.*`, `team.*`, `host.*`, or `terminal.*` key, so these all fall out.
The project tightened to this state on purpose (commits `23f623b`, `88b55d7`).

The raw telemetry carries this context for most harnesses. A prior research pass
plus a live codex sample and a HyperDX export catalogued every source; this
design maps the ones that exist.

The OpenTelemetry GenAI conventions define no GenAI-specific user attribute and
defer to the general `user.*` / `enduser.*` namespaces, which they mark as PII.
`host.*` is a standard OTel resource namespace (`host.name`, `host.id`,
`host.type`; Development stability, resource-scoped). No convention exists for a
terminal type. This connector keeps its own `coding_agent.*` namespace for the
user and team keys (matching `coding_agent.client.*`, `coding_agent.source.*`),
preserves the OTel-standard `host.name` as a resource attribute, and adds
`coding_agent.terminal.type` for the vendor terminal field.

## Decisions

- **`coding_agent.session.id` is out of scope.** Every harness's session id is
  already the source for `gen_ai.conversation.id`; none exposes a session id
  distinct from the conversation id, so a third key would duplicate an existing
  one.
- **Copilot's `enduser.pseudo.id` counts as a user id.** The id is pseudonymous
  rather than a real name or email, but the value remains copilot's only user
  identifier, so it maps to `coding_agent.user.id` like every other harness's.
- **Codex carries user identity under ChatGPT auth.** The committed codex
  fixture came from an API-key-auth session, which emits only `auth.*` booleans. A
  live ChatGPT-auth sample and codex upstream both show `user.account_id`,
  `user.email`, and `terminal.type` on every `codex.*` log event. Codex keeps
  these in its log lane (which this connector consumes) while stripping them from
  its own trace export.
- **`coding_agent.user.email` joins the design.** Codex is its only source on
  the connector's path; Pi emits `user.email` too, but on metrics the connector
  cannot process.
- **`host.name` stays a standard OTel resource attribute.** Only codex carries
  it on the connector's path. The design keeps the OTel key rather than renaming
  it to `coding_agent.host.name`.
- **`coding_agent.terminal.type` is always emitted.** Codex and claude both send
  a terminal type. A terminal type carries neither identity nor PII, so the
  identity flag does not control it.
- **One setting controls the PII fields, default on.** OpenTelemetry marks the
  user, email, and team ids as PII, and hostname can identify a machine or
  person, so `capture_identity` controls all four. The default is on because the
  request is to surface identity by default. `terminal.type` stays outside the
  flag.

## Design

### Config setting

Add `CaptureIdentity bool` with `mapstructure:"capture_identity"` to the
connector config (`internal/codex/config.go`, aliased as the component `Config`
in `config.go`). `NewDefaultConfig` sets it to `true`. `Validate` needs no new
rule. When the value is `false`, the four PII fields are absent and output
matches today's behavior for them; `coding_agent.terminal.type` still appears.

### Vocabulary

Add to `canonicalAttributeKeys` (`internal/canonical/vocabulary.go`):
`coding_agent.user.id`, `coding_agent.user.email`, `coding_agent.team.id`,
`coding_agent.terminal.type`. Add `host.name` to `canonicalResourceKeys`
(`internal/canonical/resource.go`). All go in unconditionally; emission stays
controlled by whether a normalizer writes them and, for the resource key, by the
flag passed to the resource filter.

### Config threading

The logs edge already receives config: `newLogsRouter` passes `*Config` to
`codex.New(cfg, set, next)` and `cursor.New(cfg, set, next)` (`logs.go:29-38`),
so both read `cfg.CaptureIdentity` directly. Codex now needs it — its ChatGPT
sessions carry identity.

The traces edge drops config today: `createTracesToTraces` takes
`_ component.Config` (`factory.go:36-43`) and `newTracesRouter(next)`
(`traces.go:34-36`) builds each edge with `claude.New(next)`, `genai.New(next)`,
`opencode.New(next)`, `openhands.New(next)`, `pi.New(next)`. Change the factory
to pass `cfg.(*Config)` into `newTracesRouter`, and thread the
`CaptureIdentity` bool into `claude.New`, `genai.New`, `opencode.New`, and
`openhands.New`. `pi.New` stays unchanged — Pi sends no trace-side identity.

The resource filter gains the flag: `canonical.FilterResource` takes a bool so
`host.name` survives only when identity capture is on. Every normalizer's
`FilterResource` call passes its `CaptureIdentity` value. When the flag is off
the filter drops `host.name` as today.

Each normalizer writes the identity keys onto the `invoke_agent` root span only
when the flag is on. Each normalizer writes `coding_agent.terminal.type`
whenever the raw value is present, flag or no flag.

### Per-harness mapping

Written onto the canonical `invoke_agent` root span (except `host.name`, a
resource attribute). The last column shows whether `capture_identity` controls
the key.

| Canonical key | Raw source | Rides on (raw) | Harness | Under flag |
| --- | --- | --- | --- | --- |
| `coding_agent.user.id` | `user.id` | span | claude | yes |
| `coding_agent.user.id` | `cursor.user.id` | resource | cursor | yes |
| `coding_agent.user.id` | `ai.telemetry.metadata.userId` | span | opencode | yes |
| `coding_agent.user.id` | `lmnr.association.properties.user_id` | span | openhands | yes |
| `coding_agent.user.id` | `enduser.pseudo.id` | span | copilot (genai) | yes |
| `coding_agent.user.id` | `user.account_id` | log | codex | yes |
| `coding_agent.user.email` | `user.email` | log | codex | yes |
| `coding_agent.team.id` | `cursor.team.id` | resource | cursor | yes |
| `host.name` (resource) | `host.name` | resource | codex | yes |
| `coding_agent.terminal.type` | `terminal.type` | log | codex | no |
| `coding_agent.terminal.type` | `terminal.type` | span | claude | no |

Where a source is missing on a record, the normalizer writes nothing for that
key (no empty-string attribute). Pi carries no trace-side identity, so it stays
unchanged.

### Validator

`e2e/validator/validator.go` treats any non-canonical span attribute as a leak
through `rejectGenAIContent` and the allowlist. Once the new keys join the
vocabulary they pass that check. Add a positive assertion path so an identity run
confirms `coding_agent.user.id` (and the email, team, and terminal keys where the
fixture carried the raw source) appears on the root, and that `host.name`
survives on the resource. Keep the `rejectSensitiveAttrs` vendor trio unchanged —
it never named these keys.

### Fixtures

Committed canonical fixtures for harnesses whose capture carried a raw identity
source regenerate to include the new keys. Regeneration is deterministic where a
committed raw fixture exists to replay (cursor, openhands, genai), so those need
no paid run. The codex fixture needs two fixes: the committed capture predates
both codex's websocket and turn_ttft events and its ChatGPT-auth identity, so the
plan adds a codex raw fixture (or extends the existing one) that carries
`user.account_id`, `user.email`, `terminal.type`, and resource `host.name`. All
identity values in committed fixtures are synthetic — never a real account email
or id — so no PII lands in the repository.

### Docs

Flip the identity rows in `docs/harnesses/*.md` from "dropped" to the new
mapping, including codex's `user.account_id`/`user.email`/`host.name`/
`terminal.type` and claude's `terminal.type`. Add the `capture_identity` setting
and its PII note to `docs/design.md` and the config reference. Record that
`coding_agent.session.id` stays out of scope.

### Tests

Each affected harness's `normalizer_test.go` gains a pair for the flag-controlled
keys: with `capture_identity` on, the root carries the mapped identity keys (and
the resource carries `host.name` for codex); with it off, the root carries none
and the filter drops `host.name`. A separate case shows `coding_agent.terminal.type`
appears for codex and claude regardless of the flag. The conformance tests keep
proving the allowlist. A config test covers the default (`true`) and mapstructure
decoding.

## PII and rollout

The user, email, team, and hostname fields are PII or machine-identifying.
Because `capture_identity` defaults on, a deployment that upgrades starts
emitting them without any config change — a behavior change worth a release note
and a prominent line in the config docs. Operators who want the prior behavior
set `capture_identity: false`. `coding_agent.terminal.type` ships on by default
and has no off switch, since it carries no identity.

## Out of scope

- `coding_agent.session.id` (session equals conversation on every harness today).
- Pi metrics identity (`user.name`, `user.email`, `host.name` on metrics): the
  connector has no metrics capability, so those travel a separate pipeline it
  never sees.
- A `coding_agent.user.name` key: no harness sends a name on the connector's
  trace or log path (Pi's `user.name` is metrics-only).
- Adopting the OpenTelemetry `user.*` / `enduser.*` namespaces directly for the
  user and team keys.
- Codex team or organization id: codex does not yet emit one (an open upstream
  request).

## Testing strategy

- Unit: per-harness on/off pairs for the flag-controlled keys, a
  terminal-type-always case, config default and decoding, conformance allowlist,
  resource filter with the flag on and off.
- E2e: the canonical fixture tests in `e2e/validator` cover the regenerated
  fixtures. Per the repo's per-harness rule, any harness whose live behavior
  changes needs its stack run before the PR; the plan minimizes paid runs by
  replaying committed raw fixtures where they exist and names the exceptions
  (codex needs a refreshed raw fixture with synthetic identity).
