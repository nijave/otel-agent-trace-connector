FROM node:24-trixie-slim

ARG PI_CODING_AGENT_VERSION=1.3.0
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global "@earendil-works/pi-coding-agent@${PI_CODING_AGENT_VERSION}"
# pi-telemetry dynamically imports @earendil-works/pi-coding-agent at
# session_start. pi's loader aliases only static imports, so Node resolves this
# one by walking up from /root/.pi/agent/npm, where the global install is not
# visible. /root/node_modules is on that path and outside the directory pi's
# npm install manages.
RUN mkdir -p /root/node_modules/@earendil-works \
    && ln -s "$(npm root --global)/@earendil-works/pi-coding-agent" \
        /root/node_modules/@earendil-works/pi-coding-agent

# pi resolves the packages list at startup, which materializes the extension
# under /root/.pi/agent/npm on first run inside the container.
RUN mkdir -p /root/.pi/agent
COPY settings.json /root/.pi/agent/settings.json
COPY run.sh /usr/local/bin/run-pi-e2e
RUN chmod 0555 /usr/local/bin/run-pi-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-pi-e2e"]
