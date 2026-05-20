# syntax=docker/dockerfile:1.7

# ---- Builder ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /build

# Cache modules
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X github.com/sdldev/dockpal-agent/internal/config.Version=${VERSION}" \
      -o /out/dockpal-agent .

# ---- Runtime ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S dockpal || true

COPY --from=builder /out/dockpal-agent /usr/local/bin/dockpal-agent

# Default state directory (compose files + TLS certs persist here when mounted)
RUN mkdir -p /opt/dockpal-agent

EXPOSE 9273

# Use the binary's own healthcheck — it auto-detects TLS vs plain HTTP.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["dockpal-agent", "healthcheck"]

ENTRYPOINT ["dockpal-agent"]
