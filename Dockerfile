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
# 2026-06-21: switched from alpine:3.20 to kx-base:go-vue-amd64 (Debian 13).
# Why: alpine 3.20's package index (dl-cdn.alpinelinux.org) suffers from
# intermittent SSL handshake failures from the buildx network (GFW). The
# kx-base image already provides ca-certificates + tzdata + adduser, and
# is served from registry.kxpms.cn (no upstream dependency). The size
# increase (~900MB vs 5MB) is acceptable for a server-side data plane.
# See deploy/shared/docs/base-image-strategy.md for the registry catalog.
FROM --platform=linux/amd64 registry.kxpms.cn/kx-base:go-vue-amd64

ARG GIT_SHA=""
ARG BUILD_DATE=""
ARG BUILD_SEQ="0"

# kx-base:go-vue already provides ca-certificates + tzdata + a non-root
# 'appuser' (uid=1001). The runtime runs as this user (matches the
# original alpine llmgw user spec: uid=1001, no shell). No additional
# user creation is needed.

WORKDIR /

COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
COPY --from=builder /src/web/dist /opt/llm-gateway-go/web/dist

# kx-base:go-vue defaults USER=appuser (uid=1001); the COPY --from=builder
# files are owned by root, so we need root to chown them to appuser.
USER root

# Stamp version files after COPY so the running process can read
# ./.deploy_seq, /.deploy_seq, /opt/llm-gateway-go/VERSION and
# /opt/llm-gateway-go/.deploy_seq from a single image, regardless
# of which path the runtime / post-deploy script picks.
# (chown -R so the appuser runtime can re-stamp these on post-deploy.)
RUN chown -R appuser:appuser /opt/llm-gateway-go && \
    echo "1.0.0-${GIT_SHA:-unknown}-${BUILD_DATE:-$(date -u +%Y%m%d)}" > /opt/llm-gateway-go/VERSION && \
    echo "${BUILD_SEQ:-0}" > /opt/llm-gateway-go/.deploy_seq && \
    printf '%s\n' "${BUILD_SEQ:-0}" > /.deploy_seq && \
    printf '1.0.0-%s-%s\n' "${GIT_SHA:-unknown}" "${BUILD_DATE:-$(date -u +%Y%m%d)}" > /.VERSION

USER appuser

EXPOSE 8781

ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
