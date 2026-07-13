# Agent guidance for this repository

Read this before making changes. These are hard constraints, not suggestions.

## Simplicity and test coverage come first

**Simplicity and test coverage are the primary concerns of this project.** Every
change is judged first on whether it is the simplest thing that works and whether
it is adequately tested — before cleverness, flexibility, or feature breadth.

- **YAGNI. Build only what is needed now.** Do not add configuration, abstraction,
  overlays, credential paths, or "just in case" machinery for a need nobody has
  today. Speculative generality is a defect here, not foresight.
- **Complexity is a cost that is very rarely earned.** It is almost always a
  deliberate trade-off to save time or money (e.g. a specific, measured
  performance requirement). If you believe some complexity is warranted, that is
  a human judgement: state the trade-off explicitly and let a maintainer decide.
  Do not add it unilaterally.
- **Delete unearned complexity when you find it.** Prefer removing speculative or
  over-engineered code over preserving, matching, or extending it. This codebase
  has previously accumulated over-engineering added by agents (e.g. a private-CA
  bundle apparatus nobody asked for); do not reintroduce that pattern.
- **Prefer allowlists over denylists** — enumerate what is permitted, not what to
  exclude. Denylists silently fail open as the world changes.

## Workflow

- **Validate locally before you commit and push.** Run the build, tests, linters,
  and — where practical — the same commands CI runs (`go test ./...` in each
  module, `docker compose config`, `docker build`, `goreleaser check`). Only push
  working code. Skipping local validation requires a compelling, human-made
  reason.
- **Commit atomic, related units of work and push them promptly.** Do not let a
  large pile of unrelated changes accumulate into one commit.
- **If you push something broken, fix it** — forward, or by amending and (only
  with explicit, session-scoped authorization) force-pushing. Never leave a
  broken commit on `main`.

## Layout notes

- The connector component is its own Go module under
  `connector/codingagentconnector/`; the e2e validator is the repo-root module.
  There is intentionally no `go.work` — build each module independently.
- `connector/codingagentconnector/metadata.yaml` drives mdatagen; regenerate with
  `./scripts/generate.sh` (CI fails if the generated files are stale).
- The live e2e tests are paid and opt-in; see the README.
