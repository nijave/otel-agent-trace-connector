FROM node:24-bookworm-slim

ARG CODEX_VERSION=0.144.1
RUN npm install --global "@openai/codex@${CODEX_VERSION}"

RUN mkdir -p /codex-home /work
COPY config.toml /codex-home/config.toml
COPY run.sh /usr/local/bin/run-codex-e2e
RUN chmod 0555 /usr/local/bin/run-codex-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-codex-e2e"]
