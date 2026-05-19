FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X github.com/sdldev/dockpal-agent/internal/config.Version=$(cat .version 2>/dev/null || echo dev)" -o /dockpal-agent .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /dockpal-agent /usr/local/bin/dockpal-agent

EXPOSE 9273

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:9273/agent/ping || exit 1

ENTRYPOINT ["dockpal-agent"]
