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
ARG GOPROXY=https://proxy.golang.org,direct
ARG NPM_REGISTRY=https://registry.npmjs.org/
ENV GOPROXY=${GOPROXY}
RUN GOTOOLCHAIN=auto GOPROXY=${GOPROXY} go mod download

# Build the Vue SPA first so we know web/dist/ is always fresh.
COPY web/package.json web/package-lock.json* web/
RUN cd /src/web && npm config set registry "${NPM_REGISTRY}" && npm ci --no-audit --no-fund
COPY web/ /src/web/
RUN cd /src/web && npm run build

COPY . .

# Version injection — populated by deploy scripts or manual --build-arg.
ARG GIT_SHA=""
ARG BUILD_DATE=""
ARG BUILD_SEQ="0"

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=auto \
    go build -ldflags="-s -w" -o /llm-gateway-go ./cmd/gateway

# ── Runtime stage ───────────────────────────────────────────────────────────
FROM --platform=linux/amd64 alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1001 llmgw

WORKDIR /opt/llm-gateway-go

COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
COPY --from=builder /src/web/dist ./web/dist

# Write VERSION file so /api/system/version returns real build metadata.
# Format: version-gitSHA-buildDate (parsed by admin/misc.go:parseVersionString)
ARG GIT_SHA=""
ARG BUILD_DATE=""
ARG BUILD_SEQ="0"
RUN echo "1.0.0-${GIT_SHA:-unknown}-${BUILD_DATE:-$(date -u +%Y%m%d)}" > VERSION && \
    echo "${BUILD_SEQ}" > .deploy_seq

USER llmgw

EXPOSE 8781

ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
