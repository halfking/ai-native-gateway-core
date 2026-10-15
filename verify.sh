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

# ── 本机 go 验证口径（2026-10-14 登记）─────────────────────────────────
#
# 本机 macOS SDK 27.0 与 CLT linker 不兼容：任何**走 cgo 的形态**——`go build`
# 的链接步、以及编进 SQLite 驱动的测试二进制——一律死在 clang 的
# `tapi error: malformed file` / `unknown architecture arm64e.x1-macos`。
# 2026-10-14 实测（同一 commit、同一台机）：
#     go build ./...              → RC=1（121 行 tapi/clang 报错，仅链接步失败）
#     CGO_ENABLED=0 go build ./... → RC=0
# 这是宿主工具链的结构性缺口，不是代码回归——因此**在 macOS 上**一切 go 验证钉
# cgo=0。作用域刻意按宿主划：Linux/CI 上 cgo 本来可用，cgo=0 会让 6 个 lite/sqlite
# 包的用例整片红（driver_nocgo.go 按设计 panic），所以那边一个字节都不改。
# 需要在 macOS 上强制 cgo 时用 VERIFY_CGO_ENABLED=1。
#
# 判据：tests/verify_cgo_convention_test.sh（已接入本文件下方）
if [[ "$(uname -s)" == "Darwin" ]]; then
  export CGO_ENABLED="${VERIFY_CGO_ENABLED:-0}"
fi
echo "[verify] CGO_ENABLED=${CGO_ENABLED:-<unset: 宿主默认 cgo>}"

echo "[verify] cgo convention contracts"
bash tests/verify_cgo_convention_test.sh

echo "[verify] db252 tunnel and sync shell contracts"
bash tests/db252_tunnel_test.sh

# 2026-10-07：部署脚本曾因裸 GNU `timeout` 在 macOS 上恒 127，导致 docker 探测整块
# 被静默跳过（commit df69ef0be 修掉）。契约测试本身依赖共享 SSOT、跑不进 CI，
# 因此单挂这道零外部依赖的聚焦门防同类回归。
echo "[verify] deploy timeout portability contracts"
bash tests/deploy_timeout_portability_test.sh

# 2026-10-10：252 上 pg17-disk-watch.sh 堆到 310 份副本、149 个僵死
# `docker exec ... psql`，把 15G 内存吃到 swap 100% / load 300，PG 因此
# 无法服务 154 网关（SQLSTATE 57P03、一小时 194 个 HTTP 503）。
# 根因是本目录巡检**普遍**缺单实例闸门与查询超时，属于会再次发生的形态，
# 因此单挂这道零外部依赖的聚焦门防同类回归（静态部分任何 runner 都能跑，
# 行为部分需要 flock，在 Linux 上才执行）。
echo "[verify] 252-monitor single-instance gate contracts"
bash tests/monitor_single_instance_gate_test.sh

# 2026-10-11：2533 部署轮遗留 4 个孤儿网关（stop_instance 只发 TERM 不等待、
# cutover 重试后旧 pid 失去 pidfile 引用）。这道零外部依赖门锁住
# TERM→KILL 升级与部署收尾 reap_stray_gateways 的行为（含变异验证）。
echo "[verify] deploy-local orphan gateway reap contracts"
bash tests/deploy_local_reap_test.sh

# 2026-10-13：192.168.31.211 的 Docker 形态部署。零外部依赖门锁住
# · Redis 库号（用户口中的 "tableid"）必须从 15 往下倒排，不能从 0 抢；
# · claim 持久化使重跑不漂移（否则每次部署换库 = 缓存全丢）；
# · PG 复用判定必须同时校验 PG17 与 columnar 两个维度；
# · $var 紧跟全角括号在 UTF-8 locale 会被并入变量名（unbound variable）。
echo "[verify] deploy-211 docker contracts"
bash tests/deploy_211_contract_test.sh

echo "[verify] pre-commit checks"
./scripts/pre-commit-check.sh

echo "[verify] migration channel contracts"
bash scripts/apply-db-revision-sequence_test.sh

echo "[verify] migration checksums (sql/migrations/startup vs docs/db-changelog.md)"
./scripts/verify-migration-checksums.sh --quiet

echo "[verify] full Go tests"
# 目标集保持 `./...`，**不做任何剔除/白名单**：AGENTS.md 的既有策略是「主门在本机
# 长期为红是维护者决定，不要加白名单」——加剔除等于替维护者决定哪些失败可以不看。
# 本机 cgo=0 下 SQLite 真实驱动不编入（driver_cgo.go ↔ driver_nocgo.go 双形态，
# nocgo 分支按设计 panic「lite 存储模式需 CGO 构建」），下列 6 个包在本宿主必然红，
# 它们的真实覆盖在 CI 的 cgo=1 上：
#   bg / cmd/gateway / storage / storage/factory / storage/sqlite / tests/integration
# 该名单的漂移报警在 tests/verify_cgo_convention_test.sh。
go test ./... -count=1 -timeout=300s

# 2026-10-14（r1031 §五-3 / 判据纪律 #11）：TestStorageAudit* 集成审计是三域存储
# 契约（冷读一致性/跨实例一致性/V2 元数据配对）的唯一观测器，带 -tags=integration
# 且需要一次性回环 Redis+PG——上面的 `go test ./...` 从不编译它们，此前无任何入口
# 真实执行；首次执行即抓到真缺陷（F3）。docker 可用时必跑，否则显式 SKIP。
echo "[verify] session storage audit integration suite (docker-gated)"
bash tests/session_storage_audit_gate.sh

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
