#!/usr/bin/env bash
# =====================================================================
# tests/deploy_nocgo_build_test.sh — CGO=0 build regression tests
#
# Regression guard for the 2026-09-05 incident: the dual-mode storage
# merge pulled storage/sqlite (mattn/go-sqlite3, cgo-only) into the
# cmd/gateway dependency graph (cmd/gateway → storage/factory →
# storage/sqlite) while deploy scripts pin CGO_ENABLED=0 — the build
# broke, and a bash set -e quirk (assignment context swallowing the
# failure) let a stale binary ship under a fresh version number.
#
# Fixed by splitting storage/sqlite on build tags:
#   driver_cgo.go  (//go:build cgo)  — real driver registration
#   driver_nocgo.go (//go:build !cgo) — sentinel driver name
# so the CGO=0 graph no longer contains the cgo import edge.
#
# This harness keeps that fix from silently regressing:
#
#   - nocgo_build: CGO_ENABLED=0 GOOS=linux GOARCH=arm64 build of
#     ./cmd/gateway succeeds and produces a non-empty binary
#     (the canonical deploy build contract)
#   - cgo_only_regression: a scratch package that mimics the pre-fix
#     anti-pattern (imports go-sqlite3 and uses an API beyond the
#     upstream !cgo stub, no //go:build cgo guard) must FAIL the
#     CGO_ENABLED=0 build — and must compile with cgo enabled —
#     proving this harness detects the regression class
#   - dep_graph: CGO_ENABLED=0 `go list -deps ./cmd/gateway` contains
#     no github.com/mattn/go-sqlite3 and no package with cgo files;
#     any commit that re-imports go-sqlite3 without a cgo build-tag
#     guard turns this red
#
# See fix commit bc6e696b3 ("CGO=0 构建断裂修复 + 部署脚本陈旧二进制防线").
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }
log_info() { printf '  \033[0;34mINFO\033[0m %s\n' "$1"; }

# Scratch package lives inside the module (so vendored go-sqlite3
# resolves) but is dot-prefixed, so `go build ./...` / `go test ./...`
# patterns and gofmt never pick it up. Removed on exit via trap.
SCRATCH_DIR="$REPO_ROOT/.tmp-nocgo-regression-$$"
BUILD_TMP=""

cleanup() {
  [[ -n "$BUILD_TMP" ]] && rm -rf "$BUILD_TMP"
  rm -rf "$SCRATCH_DIR"
}
trap cleanup EXIT INT TERM

if ! command -v go >/dev/null 2>&1; then
  echo "FATAL: go not found in PATH" >&2
  exit 1
fi

# ---- tests --------------------------------------------------------------

# Canonical deploy build contract: the pure-static linux/arm64 gateway
# binary must compile, and the artifact must exist and be non-empty.
# (The incident shipped a stale binary precisely because a build
# failure was not tied to an artifact check.)
test_nocgo_build_cmd_gateway() {
  echo "── nocgo_build ──"
  local bin rc
  BUILD_TMP="$(mktemp -d -t kx-nocgo-build.XXXXXX)"
  bin="$BUILD_TMP/gateway"

  ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
      go build -o "$bin" ./cmd/gateway ) >/dev/null 2>&1
  rc=$?
  if (( rc == 0 )); then
    log_pass "CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/gateway exits 0"
  else
    log_fail "CGO_ENABLED=0 build of ./cmd/gateway broke (rc=$rc) — cgo-only package re-entered the graph?"
  fi

  if [[ -f "$bin" ]]; then
    log_pass "build artifact exists: gateway"
  else
    log_fail "build artifact missing: $bin"
  fi
  local size
  size=$(wc -c <"$bin" 2>/dev/null | tr -d ' ' || echo 0)
  if [[ "${size:-0}" -gt 0 ]]; then
    log_pass "build artifact is non-empty (${size} bytes)"
  else
    log_fail "build artifact is empty (0 bytes)"
  fi

  rm -rf "$BUILD_TMP"; BUILD_TMP=""
}

# Proves the harness can catch the regression: a package written the
# way storage/sqlite looked BEFORE fix bc6e696b3 (unprotected import of
# go-sqlite3 + use of *sqlite3.SQLiteConn.Exec, which the upstream
# !cgo stub in static_mock.go does not implement) must fail the
# CGO_ENABLED=0 build. The cgo-enabled control build proves the scratch
# package is valid Go — i.e. the failure above is CGO-specific, not a
# broken fixture.
test_cgo_only_package_fails_nocgo() {
  echo "── cgo_only_regression ──"
  mkdir -p "$SCRATCH_DIR"
  # Mimics pre-fix storage/sqlite/schema.go driverFor(): registers a
  # driver with a ConnectHook that calls conn.Exec — an API that only
  # exists in the cgo build of go-sqlite3.
  cat >"$SCRATCH_DIR/main.go" <<'EOF'
package main

import (
	"database/sql"
	"fmt"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// regression scratch: unprotected cgo-only usage, as in pre-fix
// storage/sqlite (before the //go:build cgo / !cgo split).
func main() {
	sql.Register("nocgo-regression-scratch", &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			if _, err := conn.Exec("PRAGMA foreign_keys = ON;", nil); err != nil {
				return fmt.Errorf("scratch: %w", err)
			}
			return nil
		},
	})
}
EOF

  local err rc
  err=$( ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
      go build -o "$SCRATCH_DIR/bin-scratch" "./.tmp-nocgo-regression-$$" ) 2>&1 )
  rc=$?
  if (( rc != 0 )); then
    log_pass "unprotected cgo-only package fails CGO_ENABLED=0 build (rc=$rc)"
  else
    log_fail "unprotected cgo-only package unexpectedly built under CGO_ENABLED=0"
  fi
  # Guard against passing for the wrong reason: the failure must be the
  # missing !cgo API, not a toolchain or syntax error.
  if printf '%s' "$err" | grep -q "sqlite3"; then
    log_pass "CGO_ENABLED=0 failure names go-sqlite3 API (missing !cgo stub method)"
  else
    log_fail "CGO_ENABLED=0 failure does not mention go-sqlite3; got [$err]"
  fi

  # Control: the same package must compile with cgo enabled (native).
  # If this fails, the reverse-case assertion above is meaningless.
  ( cd "$REPO_ROOT" && CGO_ENABLED=1 \
      go build -o "$SCRATCH_DIR/bin-scratch-cgo" "./.tmp-nocgo-regression-$$" ) >/dev/null 2>&1
  rc=$?
  if (( rc == 0 )); then
    log_pass "same scratch package compiles with CGO_ENABLED=1 (failure is CGO-specific)"
  else
    log_fail "scratch package fails even with cgo enabled (rc=$rc) — fixture broken or no C toolchain"
  fi
}

# Static tripwire on the dependency graph itself. Under CGO_ENABLED=0
# the only go-sqlite3 import in the graph lives behind //go:build cgo,
# so the import edge disappears. Any commit that imports go-sqlite3
# (or any other cgo-only package) without a build-tag guard keeps the
# edge alive under CGO_ENABLED=0 and turns this red — even before the
# binary build is attempted.
test_nocgo_dep_graph_no_sqlite3() {
  echo "── dep_graph ──"
  local deps rc
  deps=$( ( cd "$REPO_ROOT" && CGO_ENABLED=0 go list -deps ./cmd/gateway ) 2>&1 )
  rc=$?
  if (( rc != 0 )); then
    log_fail "CGO_ENABLED=0 go list -deps ./cmd/gateway failed (rc=$rc): $deps"
    return
  fi

  if ! printf '%s\n' "$deps" | grep -q "github.com/mattn/go-sqlite3"; then
    log_pass "CGO_ENABLED=0 dep graph contains no github.com/mattn/go-sqlite3"
  else
    log_fail "github.com/mattn/go-sqlite3 re-entered the CGO_ENABLED=0 dep graph of ./cmd/gateway"
  fi

  local cgo_pkgs
  cgo_pkgs=$( ( cd "$REPO_ROOT" && CGO_ENABLED=0 go list -deps \
      -f '{{if .CgoFiles}}CGO:{{.ImportPath}}{{end}}' ./cmd/gateway ) 2>/dev/null | grep '^CGO:' || true)
  if [[ -z "$cgo_pkgs" ]]; then
    log_pass "CGO_ENABLED=0 dep graph contains no package with cgo files"
  else
    log_fail "packages with cgo files in CGO_ENABLED=0 dep graph: [$cgo_pkgs]"
  fi

  # Whitelist context (informational, not asserted): storage/sqlite is
  # expected to remain in the graph — it is the build-tag split that
  # protects the CGO=0 build, not removal from the graph.
  if printf '%s\n' "$deps" | grep -q "github.com/kaixuan/llm-gateway-go/storage/sqlite"; then
    log_info "storage/sqlite in dep graph; its cgo files are excluded by //go:build cgo (expected)"
  else
    log_info "storage/sqlite no longer in ./cmd/gateway dep graph (graph refactored?)"
  fi
}

# ---- runner --------------------------------------------------------------

run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_nocgo_build_test.sh — CGO=0 build regression tests"
  echo "═══════════════════════════════════════════════════════════════"
  test_nocgo_build_cmd_gateway
  test_cgo_only_package_fails_nocgo
  test_nocgo_dep_graph_no_sqlite3

  echo
  echo "───────────────────────────────────────────────────────────────"
  echo " summary: ${TESTS_PASSED} passed, ${TESTS_FAILED} failed"
  if (( TESTS_FAILED > 0 )); then
    echo " failed:"
    for n in "${FAILED_NAMES[@]}"; do echo "   - $n"; done
    return 1
  fi
  return 0
}

if [[ $# -gt 0 ]]; then
  case "$1" in
    nocgo_build)         test_nocgo_build_cmd_gateway ;;
    cgo_only_regression) test_cgo_only_package_fails_nocgo ;;
    dep_graph)           test_nocgo_dep_graph_no_sqlite3 ;;
    all|*)               run_all ;;
  esac
else
  run_all
fi
