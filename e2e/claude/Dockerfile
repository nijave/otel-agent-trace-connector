FROM node:24-bookworm-slim

ARG CLAUDE_CODE_VERSION=2.1.207
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

RUN mkdir -p /work
COPY run.sh /usr/local/bin/run-claude-e2e
RUN chmod 0555 /usr/local/bin/run-claude-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-claude-e2e"]
