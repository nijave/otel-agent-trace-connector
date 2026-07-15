FROM golang:1.25-bookworm AS builder

ARG OCB_VERSION=0.156.0
RUN go install go.opentelemetry.io/collector/cmd/builder@v${OCB_VERSION}

WORKDIR /src
COPY . .
RUN builder --config builder-config.yaml

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/dist/otelcol-coding-agents /usr/local/bin/otelcol-coding-agents
COPY collector-config.yaml /etc/otelcol-coding-agents/config.yaml
# Ports the bundled collector-config.yaml listens on: OTLP gRPC, OTLP HTTP, and
# the health_check extension.
EXPOSE 4317 4318 13133
ENTRYPOINT ["/usr/local/bin/otelcol-coding-agents"]
CMD ["--config=/etc/otelcol-coding-agents/config.yaml"]
