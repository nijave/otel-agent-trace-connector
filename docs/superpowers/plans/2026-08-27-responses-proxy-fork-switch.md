# Responses-proxy Fork Replacement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Point the Codex e2e at the actively maintained
`xeonvs/responses-proxy` line (source-built at a pinned commit), then
archive `nijave/responses-proxy` so only the newer upstream remains.

**Architecture:** Task 1 verifies the one blocking dependency (usage
capture from content chunks) in the local proxy clone. Task 2 rewrites
`e2e/responses-proxy/Dockerfile` from prebuilt-asset download to a pinned
source build. Task 3 sweeps remaining fork references and runs the full
gate. Task 4 closes out the fork repo, in that order.

**Tech Stack:** Docker multi-stage build (rust builder), git/gh, Rust
(cargo) for verification only, this repo's `./scripts/check.sh` e2e stack.

**Spec:** [docs/superpowers/specs/2026-08-27-responses-proxy-fork-switch-design.md](../specs/2026-08-27-responses-proxy-fork-switch-design.md)

## Global Constraints

- Pin (fixed unless Task 1 moves it):
  `dbfcd29c5bf4…` — full SHA `dbfcd29c5bf6d7831784d1f4fd24addc4682462f`,
  tip of `xeonvs/integration/all-prs`. `xeonvs` publishes no releases and
  its `main` mirrors dormant upstream; only `integration/all-prs` counts.
- The line's MSRV is 1.88 (xeonvs raised it for let-chains); the builder
  image must meet it.
- Proxy verification clone: `/home/nick/Documents/workspace/3rd/responses-proxy`
  (remotes `origin` = nijave, `xeonvs`, `CallOrRet` already configured).
  Leave its untracked `CLAUDE.md` alone.
- Connector repo gates: prefix every `go` command with `GOTOOLCHAIN=auto`;
  run `GOTOOLCHAIN=auto ./scripts/check.sh` before any push and require
  `ALL CHECKS PASSED`.
- Never run `git branch -d`/`-D` (a dcg hook blocks agents from ref
  deletion); never force-push.
- Archive the fork LAST — nothing may still reference
  `nijave/responses-proxy` when it happens.

---

### Task 1: Verify usage capture covers content chunks

The current prebuilt binary carries the fork's
`fix/usage-capture-on-content-chunk` patch because z.ai attaches token
usage to the final content chunk. The switch is safe only if xeonvs's
`6085090` covers that case.

**Files:**
- No connector edits; verification in the proxy clone only.

**Interfaces:**
- Produces: a go/no-go on pin `dbfcd29…` — and if no-go, the replacement
  pin SHA that includes the contributed fix.

- [ ] **Step 1: Fetch and inspect both fixes**

```bash
cd /home/nick/Documents/workspace/3rd/responses-proxy
git fetch origin xeonvs
git show 608509062e7e65839f669acca4c29b7ce026a45e
git diff main...origin/fix/usage-capture-on-content-chunk
```

Compare the guards: does `6085090` read usage from a chunk whose `choices`
is non-empty (content + usage together), or only from usage-only chunks?

- [ ] **Step 2: Confirm with the line's tests**

```bash
git checkout dbfcd29c5bf6d7831784d1f4fd24addc4682462f
cargo test 2>&1 | tail -5
rg -l "usage" tests/ src/ | head
```

Read the usage-capture tests on the pinned commit; confirm a case covers
usage arriving on a content-bearing chunk. `cargo test` must pass — that
run is the pre-adoption sanity check for the pin itself.

- [ ] **Step 3: Go / no-go**

Covered → proceed to Task 2 with pin `dbfcd29…`. Not covered → open a PR
against `xeonvs/responses-proxy` cherry-picking
`origin/fix/usage-capture-on-content-chunk` (adapting to their changed
capture code), wait for it to land on `integration/all-prs`, and move the
pin to the SHA that includes it. Everything downstream blocks on the
verified pin; report the wait rather than working around it.

### Task 2: Rewrite the e2e Dockerfile to a pinned source build

**Files:**
- Change: `e2e/responses-proxy/Dockerfile`

**Interfaces:**
- Consumes: the verified pin from Task 1.
- Produces: an image identical in runtime shape (same binary path, config,
  port, healthcheck) built from source.

- [ ] **Step 1: Replace the fetch stage with a build stage**

Replace the `FROM debian:trixie-slim AS fetch` stage and its two `RUN`
blocks with:

```dockerfile
FROM rust:1.90-slim AS build
ARG RESPONSES_PROXY_COMMIT=dbfcd29c5bf6d7831784d1f4fd24addc4682462f
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
RUN git clone https://github.com/xeonvs/responses-proxy.git /src \
    && git -C /src checkout "${RESPONSES_PROXY_COMMIT}" \
    && test "$(git -C /src rev-parse HEAD)" = "${RESPONSES_PROXY_COMMIT}"
WORKDIR /src
RUN cargo build --release --locked \
    && install -D -m 0755 target/release/responses-proxy /out/bin/responses-proxy
```

Update the runtime stage's `COPY --from=fetch` to `COPY --from=build`.
If `rust:1.90-slim` does not exist as a tag, use the lowest existing slim
tag ≥ 1.88 (`docker pull` locally to confirm before committing). If
`--locked` fails because the pinned commit ships no `Cargo.lock`, drop
`--locked` and note in the commit body that the lockfile is absent
upstream.

- [ ] **Step 2: Rewrite the header comment**

Replace the header's fork/prebuilt story: the e2e builds
`xeonvs/responses-proxy` (the maintained line; upstream `CallOrRet` went
dormant, our old fork `nijave/responses-proxy` closed down) at a pinned
commit; the commit hash is the integrity anchor; the SSE event-name fix
and content-chunk usage capture both ship in this line; buildx layer
caching bounds the compile cost to once per pin change. Keep the first
paragraph (what the proxy is for) as-is.

- [ ] **Step 3: Build the image alone**

```bash
docker build e2e/responses-proxy/ -t responses-proxy:pin-test
docker run --rm responses-proxy:pin-test responses-proxy --help
```

Expected: build succeeds; the binary runs.

### Task 3: Sweep references and run the full gate

**Files:**
- Change: `e2e/README.md` (says the proxy "builds from a pinned commit on
  a fork") and any other hit from the sweep below.

**Interfaces:**
- Consumes: Task 2's Dockerfile.
- Produces: the branch that becomes the connector PR.

- [ ] **Step 1: Sweep**

```bash
rg -in 'nijave/responses-proxy|RESPONSES_PROXY_TAG|RESPONSES_PROXY_SHA256|prebuilt-' --hidden -g '!.git' -g '!docs/superpowers/**'
```

Update every live reference (docs, compose, CI) to the new source-build
story. Dated records under `docs/superpowers/` stay untouched.

- [ ] **Step 2: Full gate**

```bash
GOTOOLCHAIN=auto ./scripts/check.sh
```

Expected: `ALL CHECKS PASSED`. The Codex e2e validator's usage assertions
are the acceptance test for the usage-capture dependency. A failure here
is a finding against the pin — report it; do not patch around it in this
repo.

- [ ] **Step 3: Commit, push, PR**

```bash
git add e2e/responses-proxy/Dockerfile e2e/README.md   # plus sweep hits
git commit -m "fix(e2e): build responses-proxy from the xeonvs line"
git push -u origin fix/responses-proxy-xeonvs-base
gh pr create --draft --title "fix(e2e): build responses-proxy from the xeonvs line" --body-file <body>
```

Body: what the xeonvs line brings (link issue #1), the usage-capture
verification outcome from Task 1, and that check.sh passed. Note the fork
archive follows the merge.

### Task 4: Close out the fork (after the connector PR merges)

**Files:**
- None here; GitHub state only. Requires maintainer sign-off on each step.

- [ ] **Step 1: Close the tracking issue**

Final comment on
[nijave/responses-proxy#1](https://github.com/nijave/responses-proxy/issues/1):
the e2e now consumes `xeonvs/responses-proxy` directly at a pinned commit;
list the adopted items; future re-surveys track pin bumps in the connector
repo. Close the issue.

- [ ] **Step 2: Disposition the upstream PR**

Comment on [CallOrRet#4](https://github.com/CallOrRet/responses-proxy/pull/4):
the fix lives on in the xeonvs line; the source fork is closing down.
Leave the PR open for upstream to decide; archiving may make it unmergeable
as-is — accepted, upstream is dormant.

- [ ] **Step 3: Archive**

```bash
gh repo archive nijave/responses-proxy --yes
```

Reversible via un-archive if the xeonvs line ever stalls and the fork
posture needs restoring.

## Explicitly out of scope

- Bumping the pinned Codex e2e version (0.144.1) — unlocked by this switch
  (the xeonvs fixes target Codex 0.149+ wire behavior) but its own task.
- Any new fork, release workflow, or prebuilt-asset pipeline.
- airly0201's ops script and ilylty's CI-only commits.
