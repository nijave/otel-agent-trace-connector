# Translates Codex's Responses API into Chat Completions so a real Codex CLI can
# run against z.ai's chat-only GLM endpoint. Codex 0.144.1 removed wire_api="chat"
# and requires the Responses API, which z.ai's coding endpoint does not speak.
# This service exists only for the Codex e2e stack; it is not part of the
# connector or any production path.
#
# Built from a pinned commit on our fork rather than the crates.io release. Two
# gaps make the published 0.1.3 unusable for capturing faithful Codex telemetry
# against z.ai:
#   1. 0.1.3 emits data-only SSE frames (no `event:` name line), so Codex logs
#      every streamed event as kind "message" and never sees `response.completed`.
#      Upstream v0.2.0 sets the SSE event name, which turn-completion needs.
#   2. v0.2.0 still only captures token usage from a trailing usage-only chunk
#      (choices empty); z.ai attaches usage to the final content chunk, so usage
#      was dropped. Our fork patch captures usage from any chunk that carries it.
# Fork branch: fix/usage-capture-on-content-chunk (PR-able back upstream).
FROM rust:1-slim-trixie AS build
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG RESPONSES_PROXY_REF=d2b5d0456ec00d30dbd718c0b6c95804c0aba5cc
# --locked builds against the committed Cargo.lock, so the pinned rev pins the whole
# dependency graph; without it a new semver-compatible crate release can break this
# build with no change on our side.
RUN cargo install responses-proxy \
        --git https://github.com/nijave/responses-proxy.git \
        --rev "${RESPONSES_PROXY_REF}" \
        --locked \
        --root /out

FROM debian:trixie-slim
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/bin/responses-proxy /usr/local/bin/responses-proxy
COPY config.yaml /app/config.yaml
WORKDIR /app
EXPOSE 3000
HEALTHCHECK --interval=1s --timeout=2s --retries=30 \
    CMD curl --fail --silent http://localhost:3000/health || exit 1
CMD ["responses-proxy", "--config", "/app/config.yaml"]
