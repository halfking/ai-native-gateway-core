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

echo "[verify] db252 tunnel and sync shell contracts"
bash tests/db252_tunnel_test.sh

echo "[verify] pre-commit checks"
./scripts/pre-commit-check.sh

echo "[verify] migration channel contracts"
bash scripts/apply-db-revision-sequence_test.sh

echo "[verify] migration checksums (sql/migrations/startup vs docs/db-changelog.md)"
./scripts/verify-migration-checksums.sh --quiet

echo "[verify] full Go tests"
go test ./... -count=1 -timeout=300s

echo "[verify] privacy compliance tests"
./scripts/verify-privacy-compliance.sh

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
  # pnpm 是官方路径（CI/发布），npm 并行受支持；选哪个由 scripts/lib/node-pm.sh
  # 单点决定，不在这里各写一份。WEB_PM=pnpm|npm 可强制。
  # shellcheck source=scripts/lib/node-pm.sh
  source "$REPO_ROOT/scripts/lib/node-pm.sh"
  echo "[verify] frontend lockfile sync gate"
  ./scripts/check-frontend-lockfiles.sh
  echo "[verify] frontend install"
  pm_install web
  echo "[verify] frontend typecheck"
  pm_run typecheck web
  echo "[verify] frontend tests"
  pm_run test web
  echo "[verify] frontend static gates (responsive breakpoints + el-* imports)"
  pm_run responsive:check web
  pm_run element:check web
  echo "[verify] frontend color token gate (rule 12 P0 + baseline)"
  pm_run color:check web
  echo "[verify] frontend build"
  pm_run build web
fi

if rg -n '^(<<<<<<<|>>>>>>>)' --glob '!vendor/**' --glob '!web/node_modules/**' .; then
  echo "[verify] merge conflict marker found" >&2
  exit 1
fi

echo "[verify] PASS"
