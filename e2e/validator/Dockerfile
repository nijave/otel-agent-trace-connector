FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY e2e/validator ./e2e/validator
RUN CGO_ENABLED=0 go build -o /validator ./e2e/validator

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /validator /validator
ENTRYPOINT ["/validator"]
