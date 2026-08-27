# Responses-proxy fork switch — design

**Date:** 2026-08-27
**Status:** approved for planning (maintainer call, 2026-08-27)
**Plan:** [../plans/2026-08-27-responses-proxy-fork-switch.md](../plans/2026-08-27-responses-proxy-fork-switch.md)

## Motivation

The Codex e2e stack runs against a prebuilt `responses-proxy` binary from
`nijave/responses-proxy` (`e2e/responses-proxy/Dockerfile` pins release
`prebuilt-d2b5d04`, sha256 `f14a17a2…`). That fork currently tracks the true
upstream `CallOrRet/responses-proxy` (tip `70e1825`, dormant since
2026-06-22) plus one CI commit (`3a0af2f`, linux-only release builds).

A fork survey (2026-08-24, recorded in tracking issue
[nijave/responses-proxy#1](https://github.com/nijave/responses-proxy/issues/1))
found `xeonvs/responses-proxy` under active development: 20 unique commits on
`integration/all-prs`, 14 of them high-relevance Codex-compatibility fixes —
`additional_tools`/code-mode support, tool-registry loss on continuation
turns, WS history under `store:false`, compaction timeout and trigger fixes,
request decompression, body/message caps, streamed usage capture, gpt-5.6
reasoning tiers and ladder, structured-output streaming, preamble
persistence, hosted-tool filtering, and session-store isolation. The
maintainer decided on 2026-08-27 to switch the base to that line.

## Decision record

The survey session recommended cherry-picking individual fixes rather than a
wholesale switch, because the xeonvs `integration/all-prs` branch mixes
unreviewed, partly AI-co-authored merge work, and adopting it wholesale
inherits configuration choices without review. The maintainer chose the
switch anyway; this design keeps the switch while answering each concern:

- **Merge, don't repoint or rebase.** `nijave/responses-proxy` stays the
  release vehicle and review point; a merge commit preserves both histories
  and keeps future pulls from either line cheap. Pointing the Dockerfile at
  `xeonvs` directly would surrender release control (that fork publishes no
  prebuilt assets); rebasing would rewrite published history.
- **Review the whole delta before it lands.** The merge PR's diff against
  current `main` is the review surface the survey session said the branch
  never had.
- **Pin an exact SHA.** The merge target is
  `dbfcd29c5bf6d7831784d1f4fd24addc4682462f` (branch tip since 2026-08-19),
  not a moving branch.
- **The connector e2e is the compatibility net.** `./scripts/check.sh`
  builds the proxy image and runs the Codex e2e against the pinned Codex
  0.144.1 before the new pin merges.

A same-day variant explored replacing the fork outright — building `xeonvs`
from source in the e2e image and archiving `nijave/responses-proxy`. The
maintainer dropped it: xeonvs publishes no releases and its `main` just
mirrors dormant upstream, so direct consumption would give up the
prebuilt-binary pipeline (PR #53) and return a per-pin Rust compile to CI.
The fork stays as the release vehicle and aligns to the xeonvs line instead.

## Goals

- Adopt the xeonvs line as the effective code base of the proxy the e2e uses.
- Keep `nijave/responses-proxy` as the pin source and release vehicle.
- Reconcile the fork's own unmerged fix (`fix/usage-capture-on-content-chunk`,
  upstream PR CallOrRet#4) with xeonvs's counterpart (`6085090`).
- Cut a new prebuilt release and bump the connector's pin to it.
- Update tracking issue #1 so the ranked list reflects what landed.

## Non-goals

- Bumping the pinned Codex e2e version (0.144.1). The xeonvs fixes target
  Codex 0.149+ wire behaviors, so this switch is the prerequisite; the bump
  itself stays its own task.
- Merging airly0201 or ilylty content: airly0201's `additional_tools` fix
  duplicates xeonvs's, its other commit is personal ops tooling; ilylty's
  commits are CI-only. Issue #1 keeps tracking them.
- Abandoning or archiving `nijave/responses-proxy`.

## Merge mechanics and known decision points

- **Expected conflict surface:** `.github/workflows/` — our `3a0af2f`
  (linux-only release) against xeonvs's `741e490` (MSRV 1.88, Windows
  checkout fix). Resolution rule: keep our release workflow, take their CI
  test/MSRV changes.
- **Compaction id prefix:** our code uses `rcmp_`; xeonvs's `96e15a4`
  renames `comp_` → `cmp_` (OpenAI's real prefix). Issue #1 flags `rcmp_`
  as carrying the same `invalid_id_prefix` rejection risk. Resolution rule:
  unify on `cmp_` unless the proxy's tests or the e2e show a history
  incompatibility, in which case keep `rcmp_` and record why in the PR.
- **Session-store namespace (`d82a64b`):** verify the isolation header stays
  opt-in so defaults keep behaving as before.
- **Usage capture (blocking, not optional):** the current pinned release
  (`prebuilt-d2b5d04`) already ships our `fix/usage-capture-on-content-chunk`
  patch — the z.ai e2e depends on usage arriving on the final content chunk,
  and a release cut from the merge without that behavior would regress it.
  Keep xeonvs's `6085090` from the merge, diff it against our fix, and
  cherry-pick ours on top unless theirs demonstrably covers content-bearing
  chunks; the e2e usage assertions are the acceptance test. Upstream PR
  CallOrRet#4 stays open regardless — that is upstream's queue, not ours.

## Validation

- Proxy repo: the checks its CI runs (`cargo test`, formatting/lint jobs per
  `.github/workflows/`), before and after the merge.
- Release: the existing workflow produces the linux asset; verify the
  published sha256 by downloading the asset and hashing it.
- Connector repo: `GOTOOLCHAIN=auto ./scripts/check.sh` must end
  `ALL CHECKS PASSED` with the new tag and sha256 in
  `e2e/responses-proxy/Dockerfile`.

## Risks

- **Unreviewed, partly AI-co-authored commits** — mitigated by whole-delta
  review, the proxy test suite, and the connector e2e.
- **Future divergence** — xeonvs keeps moving; periodic re-merges and issue
  #1 re-surveys are the follow-up cadence.
- **Behavioral drift the e2e can't see** — the e2e exercises Codex 0.144.1
  wire shapes only; fixes aimed at 0.149+ behaviors stay unexercised until
  the Codex pin bump. Accepted: they are additive to shapes the current pin
  never sends.
