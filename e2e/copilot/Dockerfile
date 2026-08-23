FROM node:24-bookworm-slim

ARG COPILOT_CLI_VERSION=1.0.80
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
# The official npm package is @github/copilot (@github/copilot-cli does not
# exist); its versions track the github/copilot-cli release tags one-to-one.
RUN npm install --global "@github/copilot@${COPILOT_CLI_VERSION}"

RUN mkdir -p /work
COPY run.sh /usr/local/bin/run-copilot-e2e
RUN chmod 0555 /usr/local/bin/run-copilot-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-copilot-e2e"]
