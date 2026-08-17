# Local Gateway Base Image

## Change

Use the managed `registry.kxpms.cn/kx-base:go-vue-alpine-slim-v2` image in
`Dockerfile.local-arm64` instead of installing runtime packages from the public
Alpine repository during every local build.

## Why

The public Alpine package endpoint is not reliable in the local build
environment. The managed base image is already available for the target arm64
platform and provides the runtime tools and `appuser` required by the image.

## Verification

- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -ldflags='-s -w' -o .build-local/llm-gateway-go ./cmd/gateway`
- `docker build --no-cache -f Dockerfile.local-arm64 -t r112-gateway:local-arm64 .`
- `docker compose -f docker-compose.local-r112.yml up -d --no-deps --force-recreate gateway`
- `curl -fsS http://127.0.0.1:8781/healthz`

## Rollback

Revert this commit to restore the previous local Dockerfile. No remote
deployment or database migration is part of this change.
