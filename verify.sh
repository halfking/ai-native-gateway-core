#!/usr/bin/env bash
# verify.sh — unified local/CI verification gate for llm-gateway-go.
#
# Usage:
#   ./verify.sh                 # backend gate + govulncheck
#   ./verify.sh --web            # backend gate + frontend typecheck/build
#   ./verify.sh --skip-govulncheck  # diagnostic only; never use as release evidence

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

RUN_WEB=false
SKIP_GOVULNCHECK=false
for arg in "$@"; do
  case "$arg" in
    --web) RUN_WEB=true ;;
    --skip-govulncheck) SKIP_GOVULNCHECK=true ;;
    -h|--help)
      sed -n '2,9p' "$0"
      exit 0
      ;;
    *)
      echo "unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

echo "[verify] pre-commit checks"
./scripts/pre-commit-check.sh

echo "[verify] full Go tests"
go test ./... -count=1 -timeout=300s

echo "[verify] Go vet"
go vet ./...

echo "[verify] gateway build"
go build ./cmd/gateway

if [[ "$SKIP_GOVULNCHECK" == true ]]; then
  echo "[verify] WARNING: govulncheck skipped; result is diagnostic only" >&2
else
  ./scripts/govulncheck.sh
fi

if [[ "$RUN_WEB" == true ]]; then
  echo "[verify] frontend install"
  (cd web && pnpm install --frozen-lockfile)
  echo "[verify] frontend typecheck"
  (cd web && pnpm run typecheck)
  echo "[verify] frontend tests"
  (cd web && pnpm run test)
  echo "[verify] frontend build"
  (cd web && pnpm run build)
fi

if rg -n '^(<<<<<<<|>>>>>>>)' --glob '!vendor/**' --glob '!web/node_modules/**' .; then
  echo "[verify] merge conflict marker found" >&2
  exit 1
fi

echo "[verify] PASS"
