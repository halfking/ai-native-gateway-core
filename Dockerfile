# Multi-stage build for llm-gateway-go data plane
# Build with: docker build -t kx-llm-gateway-go:latest .
#
# This Dockerfile is self-contained: the builder stage compiles the Go
# binary AND builds the Vue SPA from source. The resulting image is
# reproducible regardless of whether web/dist/ is pre-built in the
# build context.

# ── Build stage ──────────────────────────────────────────────────────────────
FROM --platform=linux/amd64 registry.kxpms.cn/kx-base:go-vue AS builder

# Defensive: kx-base:go-vue already provides git/ca-certificates, nodejs + npm.
# Verify availability; fail fast if any are missing.
RUN for cmd in git node npm; do command -v "$cmd" >/dev/null 2>&1 || (echo "ERROR: $cmd not found in base image" && exit 1); done

# kx-base:go-vue runs as non-root 'appuser' — switch back to root for build
USER root

WORKDIR /src
COPY go.mod go.sum ./
ARG GOTOOLCHAIN=auto
# GFW blocks proxy.golang.org (Google IP 142.251.33.209) — use goproxy.cn
# (Qiniu CDN) as primary. See AGENTS.md 2026-05-12 "key learning".
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ARG NPM_REGISTRY=https://registry.npmmirror.com/
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
FROM --platform=linux/amd64 docker.m.daocloud.io/library/alpine:3.20

ARG GIT_SHA=""
ARG BUILD_DATE=""
ARG BUILD_SEQ="0"

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1001 llmgw

WORKDIR /

COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
COPY --from=builder /src/web/dist /opt/llm-gateway-go/web/dist

# Stamp version files after COPY so the running process can read
# ./.deploy_seq, /.deploy_seq, /opt/llm-gateway-go/VERSION and
# /opt/llm-gateway-go/.deploy_seq from a single image, regardless
# of which path the runtime / post-deploy script picks.
RUN echo "1.0.0-${GIT_SHA:-unknown}-${BUILD_DATE:-$(date -u +%Y%m%d)}" > /opt/llm-gateway-go/VERSION && \
    echo "${BUILD_SEQ:-0}" > /opt/llm-gateway-go/.deploy_seq && \
    printf '%s\n' "${BUILD_SEQ:-0}" > /.deploy_seq && \
    printf '1.0.0-%s-%s\n' "${GIT_SHA:-unknown}" "${BUILD_DATE:-$(date -u +%Y%m%d)}" > /.VERSION

USER llmgw

EXPOSE 8781

ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
