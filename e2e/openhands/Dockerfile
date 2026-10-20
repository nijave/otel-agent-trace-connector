FROM python:3.12-slim

# openhands-sdk and openhands-tools release in lockstep (verified on PyPI:
# both at the same version), and openhands-sdk does not depend on
# openhands-tools, so both get pinned to one version argument. The ARG default
# is the pin; OPENHANDS_SDK_VERSION overrides it at compose build time.
ARG OPENHANDS_SDK_VERSION=1.56.0
# The terminal tool drives commands through a tmux session, so the tmux
# binary must exist in the image; libtmux needs a UTF-8 locale.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
        tmux \
    && rm -rf /var/lib/apt/lists/*
ENV LC_ALL=C.UTF-8
ENV LANG=C.UTF-8
RUN pip install --no-cache-dir \
    "openhands-sdk==${OPENHANDS_SDK_VERSION}" \
    "openhands-tools==${OPENHANDS_SDK_VERSION}"

COPY run.sh /usr/local/bin/run-openhands-e2e
RUN chmod 0555 /usr/local/bin/run-openhands-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-openhands-e2e"]
