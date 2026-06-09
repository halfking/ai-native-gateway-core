# Multi-stage build for llm-gateway-go data plane
# Build with: docker build -t kx-llm-gateway-go:latest .
#
# This Dockerfile is self-contained: the builder stage compiles the Go
# binary AND builds the Vue SPA from source. The resulting image is
# reproducible regardless of whether web/dist/ is pre-built in the
# build context.

# ── Build stage ──────────────────────────────────────────────────────────────
FROM --platform=linux/amd64 golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates nodejs npm

WORKDIR /src
COPY go.mod go.sum ./
ARG GOTOOLCHAIN=auto
RUN GOTOOLCHAIN=auto go mod download

# Build the Vue SPA first so we know web/dist/ is always fresh.
COPY web/package.json web/package-lock.json* web/
RUN cd /src/web && npm ci --no-audit --no-fund
COPY web/ /src/web/
RUN cd /src/web && npm run build

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=auto \
    go build -ldflags="-s -w" -o /llm-gateway-go ./cmd/gateway

# ── Runtime stage ───────────────────────────────────────────────────────────
FROM --platform=linux/amd64 alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1001 llmgw

WORKDIR /opt/llm-gateway-go

COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
COPY --from=builder /src/web/dist ./web/dist

USER llmgw

EXPOSE 8781

ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
