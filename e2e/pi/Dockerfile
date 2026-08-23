FROM node:24-trixie-slim

ARG PI_CODING_AGENT_VERSION=0.84.2
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global "@earendil-works/pi-coding-agent@${PI_CODING_AGENT_VERSION}"

# pi resolves the packages list at startup, which materializes the extension
# under /root/.pi/agent/npm on first run inside the container.
RUN mkdir -p /root/.pi/agent
COPY settings.json /root/.pi/agent/settings.json
COPY models.json /root/.pi/agent/models.json
COPY run.sh /usr/local/bin/run-pi-e2e
RUN chmod 0555 /usr/local/bin/run-pi-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-pi-e2e"]
