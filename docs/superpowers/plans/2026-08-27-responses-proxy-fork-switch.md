# Responses-proxy Fork Switch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the actively maintained `xeonvs/responses-proxy` line the code
base of the prebuilt proxy binary the Codex e2e uses, via a reviewed merge
into `nijave/responses-proxy`, a new prebuilt release, and a pin bump here.

**Architecture:** Two repos. Tasks 1-4 operate on the local clone of
`nijave/responses-proxy` (Rust): merge `xeonvs/integration/all-prs` at a
pinned SHA, resolve the two known decision points, reconcile the fork's own
usage-capture fix, then release. Task 5 bumps
`e2e/responses-proxy/Dockerfile` in this repo. Task 6 closes the loop on the
tracking issue.

**Tech Stack:** git/gh, Rust (cargo), GitHub Actions release workflow, this
repo's `./scripts/check.sh` e2e stack.

**Spec:** [docs/superpowers/specs/2026-08-27-responses-proxy-fork-switch-design.md](../specs/2026-08-27-responses-proxy-fork-switch-design.md)

## Global Constraints

- Proxy clone: `/home/nick/Documents/workspace/3rd/responses-proxy`
  (remotes already configured: `origin` = nijave, `xeonvs`, `CallOrRet`,
  plus the other survey forks). Leave its untracked `CLAUDE.md` alone.
- Merge target SHA (fixed): `dbfcd29c5bf6d7831784d1f4fd24addc4682462f`
  (`xeonvs/integration/all-prs` tip, 2026-08-19).
- Proxy gates: mirror the repo's own CI (read
  `.github/workflows/` for the authoritative job list); the floor is
  `cargo test` and `cargo fmt --check` passing.
- Connector repo gates: prefix every `go` command with `GOTOOLCHAIN=auto`;
  run `GOTOOLCHAIN=auto ./scripts/check.sh` before any push and require
  `ALL CHECKS PASSED`.
- Never run `git branch -d`/`-D` (a dcg hook blocks agents); never
  force-push either repo's `main`.
- Conflict resolution rules (from the spec): keep our release workflow
  (`3a0af2f`, linux-only) and take xeonvs's CI test/MSRV changes; unify the
  compaction id prefix on `cmp_` unless tests fail, then keep `rcmp_` and
  record why in the PR body.

---

### Task 1: Sync the proxy clone and baseline it

**Files:**
- No edits; repo state only.

**Interfaces:**
- Produces: local `main` == `origin/main` (`3a0af2f`), remotes fetched,
  and a recorded baseline test result later tasks compare against.

- [ ] **Step 1: Fetch and fast-forward**

```bash
cd /home/nick/Documents/workspace/3rd/responses-proxy
git fetch origin xeonvs CallOrRet
git checkout main
git merge --ff-only origin/main
git log --oneline -2   # expect 3a0af2f then 70e1825
```

- [ ] **Step 2: Record the baseline**

```bash
cargo test 2>&1 | tail -5
cargo fmt --check
```

Expected: tests pass; note the passing-test count in the eventual PR body.
If the baseline fails, stop — report before merging anything.

### Task 2: Merge the xeonvs line

**Files:**
- Change: whatever the merge touches (expect `src/`, `Cargo.toml`,
  `.github/workflows/`, README)

**Interfaces:**
- Consumes: the pinned SHA and conflict rules from Global Constraints.
- Produces: branch `merge/xeonvs-integration-all-prs` whose tree passes the
  proxy gates; later tasks build on this branch.

- [ ] **Step 1: Create the branch and merge**

```bash
git checkout -b merge/xeonvs-integration-all-prs
git merge --no-ff dbfcd29c5bf6d7831784d1f4fd24addc4682462f \
  -m "merge: adopt xeonvs integration/all-prs as the codex-compat base"
```

- [ ] **Step 2: Resolve conflicts by the spec's rules**

For `.github/workflows/` conflicts: keep our release workflow, take their
CI test/MSRV changes. For `src/` conflicts: prefer theirs, except the
compaction id prefix (next step decides it deliberately).

- [ ] **Step 3: Decide the compaction prefix**

```bash
rg -n "rcmp_|\"cmp_|comp_" src/
```

Unify on `cmp_` (OpenAI's real prefix). If a test in Step 5 fails on
history compatibility, keep `rcmp_` instead and record the reason in the
PR body.

- [ ] **Step 4: Verify the session-store default**

```bash
rg -n "namespace" src/ | head -20
```

Confirm the isolation header from `d82a64b` stays opt-in: with no header
set, storage paths match pre-merge behavior. If the merge changed a
default, restore the pre-merge default and note it in the PR body.

- [ ] **Step 5: Run the proxy gates**

```bash
cargo test
cargo fmt --check
```

Expected: pass, with at least the baseline count from Task 1 (the merge
brings new tests, so more is normal).

- [ ] **Step 6: Review the whole delta**

```bash
git diff origin/main...HEAD --stat
git diff origin/main...HEAD
```

Read the full diff. This is the review the xeonvs branch never had: flag
anything that phones home, changes defaults silently, or looks unrelated
to the issue-#1 list. Anything suspicious gets reverted in a follow-up
commit on this branch before the PR opens.

### Task 3: Reconcile the usage-capture fix

**Files:**
- Possibly change: the streamed-usage capture path in `src/` (exact file
  visible from the diff below)

**Interfaces:**
- Consumes: branch from Task 2; our fix lives on
  `origin/fix/usage-capture-on-content-chunk` (upstream PR CallOrRet#4).
- Produces: one resolved usage-capture implementation on the merge branch.

- [ ] **Step 1: Compare the two fixes**

```bash
git diff main...origin/fix/usage-capture-on-content-chunk
git show 608509062e7e65839f669acca4c29b7ce026a45e
```

- [ ] **Step 2: Keep or combine**

This step blocks the release: the current pinned release
(`prebuilt-d2b5d04`) already ships our patch, and the z.ai e2e depends on
usage arriving on the final content chunk — a release cut from the merge
without that behavior regresses the e2e. If xeonvs's `6085090` (already
merged in Task 2) demonstrably covers content-bearing chunks, record
"superseded by 6085090" for the PR body. Otherwise:

```bash
git cherry-pick origin/fix/usage-capture-on-content-chunk
```

Resolve overlaps keeping both guards, then re-run `cargo test`. Task 5's
`check.sh` run (the e2e usage assertions) is the acceptance test either way.

### Task 4: PR, merge, and release on nijave/responses-proxy

**Files:**
- No new edits; publishing only.

**Interfaces:**
- Produces: a new `prebuilt-<shortsha>` release tag and its asset sha256 —
  Task 5 consumes both.

- [ ] **Step 1: Push and open the PR**

```bash
git push -u origin merge/xeonvs-integration-all-prs
gh pr create -R nijave/responses-proxy \
  --title "feat: adopt xeonvs integration line as the codex-compat base" \
  --body-file /tmp/rp-merge-pr-body.md
```

Body: the decision record (issue #1, maintainer call), conflict
resolutions made (workflows, prefix, session-store), the usage-capture
outcome, and gate results. Link issue #1; do not close it.

- [ ] **Step 2: Merge after the maintainer approves**

The merge to the proxy's `main` is the maintainer's approval point — do
not self-merge without it.

- [ ] **Step 3: Cut the prebuilt release**

Read `.github/workflows/` in the proxy repo to confirm the release
trigger (the existing `prebuilt-d2b5d04` release suggests pushing a
`prebuilt-<shortsha>` tag on `main`). Trigger it for the new merge commit,
wait for the `x86_64-unknown-linux-gnu` asset, then verify:

```bash
gh release view prebuilt-<shortsha> -R nijave/responses-proxy
curl -sL <asset-url> | sha256sum
```

Record the tag and sha256.

### Task 5: Bump the connector pin

**Files:**
- Change: `e2e/responses-proxy/Dockerfile` (`RESPONSES_PROXY_TAG`,
  `RESPONSES_PROXY_SHA256`)

**Interfaces:**
- Consumes: tag + sha256 from Task 4.
- Produces: the connector e2e running against the new proxy.

- [ ] **Step 1: Branch and edit**

In `/home/nick/Documents/workspace/go/src/github.com/nijave/otel-agent-trace-connector`,
branch `fix/responses-proxy-xeonvs-base` off `origin/main`; set the new
`RESPONSES_PROXY_TAG` and `RESPONSES_PROXY_SHA256` values in
`e2e/responses-proxy/Dockerfile`.

- [ ] **Step 2: Run the full gate**

```bash
GOTOOLCHAIN=auto ./scripts/check.sh
```

Expected: `ALL CHECKS PASSED` — this builds the proxy image from the new
asset and runs the Codex e2e against pinned Codex 0.144.1. A failure here
is a compatibility finding: report it against the merge, do not patch
around it in this repo.

- [ ] **Step 3: Commit, push, PR**

```bash
git add e2e/responses-proxy/Dockerfile
git commit -m "fix(e2e): pin responses-proxy to the xeonvs-based release"
git push -u origin fix/responses-proxy-xeonvs-base
gh pr create --draft --title "fix(e2e): pin responses-proxy to the xeonvs-based release" --body-file <body>
```

Body: two sentences — what the new base brings (issue #1 list) and that
check.sh passed; link the proxy merge PR.

### Task 6: Update tracking issue #1

- [ ] **Step 1: Tick and annotate**

On [nijave/responses-proxy#1](https://github.com/nijave/responses-proxy/issues/1):
check every xeonvs item the merge landed, add a comment naming the merge
commit, release tag, and the usage-capture outcome, and leave the
airly0201/ilylty items and the re-survey cadence note open.

## Explicitly out of scope

- Bumping the pinned Codex e2e version (0.144.1) — unlocked by this switch
  (the xeonvs fixes target Codex 0.149+ wire behavior) but its own task.
- Upstream PR CallOrRet#4 — stays open; upstream's queue is not ours.
- airly0201's ops script and ilylty's CI-only commits — tracked in issue
  #1, deliberately not merged.
