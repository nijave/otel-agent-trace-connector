# Responses-proxy fork replacement — design

**Date:** 2026-08-27
**Status:** approved for planning (maintainer call, 2026-08-27)
**Plan:** [../plans/2026-08-27-responses-proxy-fork-switch.md](../plans/2026-08-27-responses-proxy-fork-switch.md)

## Motivation

The Codex e2e stack runs against a prebuilt `responses-proxy` binary from
`nijave/responses-proxy` (`e2e/responses-proxy/Dockerfile` pins release
`prebuilt-d2b5d04`). That fork exists for two reasons: the published crate
was unusable for faithful Codex telemetry, and the fork carries
`fix/usage-capture-on-content-chunk` (upstream PR CallOrRet#4), which the
z.ai e2e needs because z.ai attaches token usage to the final content chunk
rather than a trailing usage-only chunk.

The true upstream `CallOrRet/responses-proxy` has been dormant since
2026-06-22. A fork survey (2026-08-24, tracking issue
[nijave/responses-proxy#1](https://github.com/nijave/responses-proxy/issues/1))
found one line under active development: `xeonvs/responses-proxy`, whose
`integration/all-prs` branch (21 commits ahead of dormant upstream, tip
`dbfcd29c5bf6d7831784d1f4fd24addc4682462f`, 2026-08-19) carries 14
high-relevance Codex-compatibility fixes — `additional_tools`/code-mode,
tool-registry continuation loss, `store:false` history, compaction timeout
and trigger fixes, request decompression, body/message caps, streamed usage
capture (`6085090`, same bug class as our fork's patch), gpt-5.6 reasoning
tiers and ladder, structured-output streaming, preamble persistence,
hosted-tool filtering, and session-store isolation.

The maintainer decided on 2026-08-27 to **fully replace the fork**: the e2e
consumes the xeonvs line directly at a pinned commit, and
`nijave/responses-proxy` gets archived, leaving one dependency instead of a
fork to keep in sync.

## Decision record

- An earlier same-day design had the xeonvs line merging into
  `nijave/responses-proxy`, keeping the fork as the release vehicle. The
  maintainer superseded it: rely on the newer upstream only, archive the
  fork. This document reflects the replacement design.
- The 2026-08-24 survey session recommended cherry-picking over adoption
  because the xeonvs branch mixes unreviewed, partly AI-co-authored merge
  work. The replacement design answers those concerns without a fork:
  the pin is an exact commit (not a moving branch), the connector PR review
  reads the pinned diff against the old base, the connector e2e exercises
  the binary before the pin merges, and GitHub archiving is reversible —
  un-archive and resume the fork if the xeonvs line ever stalls or goes bad.
- `xeonvs/responses-proxy` publishes **no releases**, and their `main` just
  mirrors dormant upstream — all work sits on `integration/all-prs`. That
  sends the e2e image back to building from source at the pinned commit, giving up
  the prebuilt-binary download (PR #53). The CI buildx layer cache makes the
  ~140s Rust compile a once-per-pin cost rather than a per-run cost.

## The one blocking dependency

The current binary works against z.ai **only because** of the fork's
usage-capture patch. xeonvs's `6085090` fixes the same bug class, but the
switch is safe only if it demonstrably covers capture from chunks that
carry both content and usage. The plan verifies this first; if coverage
falls short, the fix goes to xeonvs as a PR and the pin moves to the commit
that includes it. No pin change lands without the e2e proving usage arrives.

## Goals

- `e2e/responses-proxy/Dockerfile` builds `xeonvs/responses-proxy` from
  source at pinned commit `dbfcd29c5bf4…` (or a later SHA that includes a
  verified usage-capture fix), with the commit hash as the integrity anchor.
- Verify or contribute the usage-capture behavior before switching.
- Close out tracking issue #1 and record the dependency change in the e2e
  docs.
- Archive `nijave/responses-proxy` once nothing references it.

## Non-goals

- Maintaining any fork, release workflow, or prebuilt-asset pipeline.
- Bumping the pinned Codex e2e version (0.144.1) — the xeonvs fixes target
  Codex 0.149+ wire behavior, so this switch is the prerequisite, but the
  bump stays its own task.
- Merging airly0201 or ilylty content (duplicate and CI-only respectively).

## Mechanics

- Builder stage: `rust` image satisfying the line's MSRV (1.88), `git clone`
  + `checkout <pinned commit>` + an explicit `rev-parse HEAD` comparison, then
  `cargo build --release --locked`. A git commit hash is content-addressed,
  so it replaces a separate tarball checksum.
- Runtime stage, config, healthcheck: unchanged.
- The Dockerfile header comment carries the provenance story: why xeonvs,
  why source-build, where the ranked fix list lives, and the un-archive
  escape hatch.

## Endgame for the fork

Ordering matters: the connector PR merges first, then the fork repo closes
down.

1. Final comment on issue #1: strategy changed to consuming the xeonvs line
   directly; list the items the switch adopts; close the issue.
2. Comment on upstream PR CallOrRet#4 noting the fix lives on in the xeonvs
   line and the fork will close down. Archiving likely prevents upstream
   from merging #4 as-is; accepted — upstream is dormant and the fix ships
   via xeonvs.
3. `gh repo archive nijave/responses-proxy` (reversible).
4. The local clone at `~/Documents/workspace/3rd/responses-proxy` stays as
   a working copy; repoint its default remote at xeonvs if it sees reuse.

## Risks

- **No control over the only maintained line** — a single other person's
  side project, now without even a fork buffer. Mitigations: exact-SHA
  pins mean nothing changes under us; full git history survives in every
  clone; un-archiving the fork restores the old posture in one click.
- **Behavioral drift the e2e can't see** — the e2e exercises Codex 0.144.1
  wire shapes only; fixes aimed at 0.149+ stay unexercised until the Codex
  pin bump. Accepted: additive to shapes the current pin never sends.
- **Compile time returns to CI** — bounded by buildx layer caching keyed on
  the pinned commit; rebuilds happen only when the pin moves.
- **Usage-capture regression** — the blocking dependency above; the e2e
  validator's usage assertions are the acceptance test.
