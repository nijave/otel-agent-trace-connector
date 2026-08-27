# Translates Codex's Responses API into Chat Completions so a real Codex CLI can
# run against z.ai's chat-only GLM endpoint. Codex 0.144.1 removed wire_api="chat"
# and requires the Responses API, which z.ai's coding endpoint does not speak.
# This service exists only for the Codex e2e stack; it is not part of the
# connector or any production path.
#
# Downloaded as a prebuilt binary from a pinned release of our fork rather than
# compiled from crates.io source here. The fork commit is required because two
# gaps make the published 0.1.3 unusable for capturing faithful Codex telemetry
# against z.ai:
#   1. 0.1.3 emits data-only SSE frames (no `event:` name line), so Codex logs
#      every streamed event as kind "message" and never sees `response.completed`.
#      Upstream v0.2.0 sets the SSE event name, which turn-completion needs.
#   2. v0.2.0 still only captures token usage from a trailing usage-only chunk
#      (choices empty); z.ai attaches usage to the final content chunk, so usage
#      was dropped. The pinned release captures usage from any chunk carrying it.
# Since prebuilt-480cfd4 the fork's base is the xeonvs integration line
# (nijave/responses-proxy#2), which ships the usage fix natively (with our
# regression test) plus Codex-compat work targeting 0.149+ wire behavior.
#
# The tag names one immutable fork commit; the fork's release workflow compiled
# it once with a locked Cargo.lock, so the whole dependency graph stays pinned.
# Pulling the finished binary replaces a ~140s Rust compile on every CI run with
# a checksum-verified download. Refresh the tag and sha256 together when the fork
# commit changes.
FROM debian:trixie-slim AS fetch
ARG RESPONSES_PROXY_TAG=prebuilt-480cfd4
ARG RESPONSES_PROXY_TARGET=x86_64-unknown-linux-gnu
ARG RESPONSES_PROXY_SHA256=84cc318237a0a7bd6eda1212ce8dae98c9f335f890b5f82fb15802bc427375c5
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN base="responses-proxy-${RESPONSES_PROXY_TAG}-${RESPONSES_PROXY_TARGET}" \
    && curl --fail --location --silent --show-error \
        --output /tmp/responses-proxy.tar.gz \
        "https://github.com/nijave/responses-proxy/releases/download/${RESPONSES_PROXY_TAG}/${base}.tar.gz" \
    && echo "${RESPONSES_PROXY_SHA256}  /tmp/responses-proxy.tar.gz" | sha256sum --check --strict \
    && tar -xzf /tmp/responses-proxy.tar.gz -C /tmp \
    && install -D -m 0755 "/tmp/${base}/responses-proxy" /out/bin/responses-proxy

FROM debian:trixie-slim
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=fetch /out/bin/responses-proxy /usr/local/bin/responses-proxy
COPY config.yaml /app/config.yaml
WORKDIR /app
EXPOSE 3000
HEALTHCHECK --interval=1s --timeout=2s --retries=30 \
    CMD curl --fail --silent http://localhost:3000/health || exit 1
CMD ["responses-proxy", "--config", "/app/config.yaml"]
