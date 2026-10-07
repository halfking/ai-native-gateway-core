#!/usr/bin/env bash
# verify.sh — unified local/CI verification gate for llm-gateway-go.
#
# Usage:
#   ./verify.sh                 # backend gate + govulncheck
#   ./verify.sh --web            # backend gate + frontend typecheck/build
#                                # (CHROME_BIN 已设置时额外跑 web-mobile layout-audit/settle 正控)
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

# 2026-10-07：部署脚本曾因裸 GNU `timeout` 在 macOS 上恒 127，导致 docker 探测整块
# 被静默跳过（commit df69ef0be 修掉）。契约测试本身依赖共享 SSOT、跑不进 CI，
# 因此单挂这道零外部依赖的聚焦门防同类回归。
echo "[verify] deploy timeout portability contracts"
bash tests/deploy_timeout_portability_test.sh

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

echo "[verify] build-tag compile matrix (every tag configuration)"
# 上面那条 go vet 不带 tag ⇒ 只在某个 tag 下才参与编译的文件**无人编译**。
# 2026-10-02 实测：admin 包在 -tags=integration 下编译不过（undefined:
# v1DirectTables），而默认配置与主干 CI 全绿。详见脚本文件头。
./scripts/check-build-tags.sh

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
  echo "[verify] web-mobile install"
  pm_install web-mobile
  echo "[verify] web-mobile unit gates (contrast/token ratchets + view regressions)"
  pm_run test web-mobile
  echo "[verify] web-mobile static-gate selftests (node-only)"
  pm_run gate:selftest web-mobile
  # R50-A2 遗留 #3 收口：layout-audit / settle 两条正控的判据跑在真实浏览器里，
  # node-only 门代替不了。CI runner 装 Chrome 后注入 CHROME_BIN 即自动升级为必过门；
  # 未注入时显式跳过并说明——不静默、也不假装跑过。
  if [[ -n "${CHROME_BIN:-}" ]]; then
    echo "[verify] web-mobile layout-audit positive control (Chrome: $CHROME_BIN)"
    pm_run audit:selftest web-mobile
    echo "[verify] web-mobile settle positive control (Chrome: $CHROME_BIN)"
    pm_run audit:settle web-mobile
  else
    echo "[verify] SKIP web-mobile layout-audit/settle positive controls (CHROME_BIN 未设置; runner 安装 Chrome 并导出 CHROME_BIN 后自动接入)"
  fi
fi

if rg -n '^(<<<<<<<|>>>>>>>)' --glob '!vendor/**' --glob '!web/node_modules/**' .; then
  echo "[verify] merge conflict marker found" >&2
  exit 1
fi

echo "[verify] PASS"
