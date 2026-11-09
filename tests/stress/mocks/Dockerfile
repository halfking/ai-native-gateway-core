# Mock upstream image for the mock-only stress stack.
#
# main.go is stdlib-only, so the build needs no go.mod, no module download,
# and no network. GO_BASE_IMAGE/RUNTIME_BASE_IMAGE are ARGs so the same
# recipe builds on the local arm64 host and on 211 (amd64) with the
# registry-pinned base images the deploy engine already uses.
ARG GO_BASE_IMAGE=golang:1.27-alpine
ARG RUNTIME_BASE_IMAGE=alpine:3.22

FROM ${GO_BASE_IMAGE} AS build
# TARGETARCH is provided automatically by BuildKit; default keeps plain
# `docker build` working for the common case.
ARG TARGETARCH=amd64
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/mock-upstream ./main.go

FROM ${RUNTIME_BASE_IMAGE}
COPY --from=build /out/mock-upstream /usr/local/bin/mock-upstream
# busybox wget ships with alpine — used by compose healthchecks.
EXPOSE 18101
ENTRYPOINT ["/usr/local/bin/mock-upstream"]
