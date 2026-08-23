FROM node:24-trixie-slim

ARG OPENCODE_VERSION=1.18.21
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global "opencode-ai@${OPENCODE_VERSION}"

# Provider configuration. The OpenCode Go key arrives at runtime via
# {env:OPENCODE_API_KEY}; nothing secret is baked into the image.
RUN mkdir -p /root/.config/opencode
COPY opencode.json /root/.config/opencode/opencode.json

RUN mkdir -p /work
COPY run.sh /usr/local/bin/run-opencode-e2e
RUN chmod 0555 /usr/local/bin/run-opencode-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-opencode-e2e"]
