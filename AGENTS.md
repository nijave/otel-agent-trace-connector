# Agent guidance for this repository

Read this before making changes. These are hard constraints, not suggestions.

## Simplicity and test coverage come first

**Simplicity and test coverage are the primary concerns of this project.** Judge
every change first on whether a simpler working alternative exists and whether
tests cover it — before cleverness, flexibility, or feature breadth.

- **YAGNI. Build only what the task needs now.** Do not add configuration,
  abstraction, overlays, credential paths, or "just in case" machinery for a
  need nobody has today. Speculative generality is a defect here, not foresight.
- **Complexity is a cost that rarely earns its keep.** Almost always it reflects
  a deliberate trade-off to save time or money (e.g. a specific, measured
  performance need). If you think a case for complexity exists, that call
  belongs to a human: state the trade-off explicitly and let a maintainer decide.
  Do not add it unilaterally.
- **Abstraction hides complexity; it never removes it.** Every abstraction adds
  complexity that conceals or relocates other complexity — it lowers *perceived*
  complexity, never *net* complexity. "Simplify by abstraction" is a
  contradiction. The only real simplification is removal.
- **Delete unearned complexity when you find it.** Remove speculative or
  over-engineered code (features, config, overlays, code paths) rather than
  preserving, wrapping, matching, or extending it. This codebase accumulated
  over-engineering from agents in the past (e.g. a private-CA bundle apparatus
  nobody asked for); do not reintroduce that pattern.
- **Removal always comes with analysis (Chesterton's Fence).** Understand why
  something exists before deleting it; establish that the complexity earns
  nothing by investigating history, callers, and tests — never assume. If you
  cannot explain its purpose, do not remove it yet. (In this repo that analysis
  is why the CA apparatus went away but the `uint64` overflow guard and
  string/int token coercions remain — they carry real weight.)
- **Prefer allowlists over denylists** — name what you permit, not what to
  exclude. Denylists silently fail open as the world changes. (The Compose
  `environment:` block, for example, is already an allowlist of what reaches a
  container; host variables are not forwarded.)

## Workflow

- **Run `./scripts/check.sh` before you push.** It runs every unpaid check CI
  runs, in the same shape: gofmt, shell syntax and shellcheck, golangci-lint
  v2.11.4 (the version CI pins) on both modules, mdatagen freshness, vet, tests
  and race tests in both modules, Collector build and config validation, Compose
  checks with the credential-split assertions, image builds, and
  `goreleaser check`. Only push working code. Skipping the check requires a
  compelling, human-made reason.
- **Commit atomic, related units of work and push them promptly.** Do not let a
  large pile of unrelated changes accumulate into one commit.
- **If you push something broken, fix it** — forward, or by amending and (only
  with explicit, session-scoped authorization) force-pushing. Never leave a
  broken commit on `main`.

## Layout notes

- The connector component is its own Go module under
  `connector/codingagentconnector/`; the e2e validator is the repo-root module.
  The repo deliberately omits `go.work` — build each module independently.
- `connector/codingagentconnector/metadata.yaml` drives mdatagen; regenerate with
  `./scripts/generate.sh` (CI fails if the generated files are stale). mdatagen is
  pinned via `internal/tools` (a `tool` directive), not `go run tool@version`
  (which fails on that module's replace directives).
- The e2e stacks share `compose.e2e-base.yaml` via `include`. Do not override an
  included service (some Compose versions reject it — `conflicts with imported
  resource`); parameterize by env instead (the validator keys off `E2E_AGENT`).
- Local Docker/Compose versions differ from CI's — check compose/Docker changes
  with the commands CI runs, and don't trust a green local `docker compose
  config` alone.
- Live e2e runs cost real money and stay opt-in; see the README.
