#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# -----------------------------------------------------------------------------
# File:          scripts/audit/measure-realdb-legs.sh
# Purpose:       Measure the real-database test legs (真库腿) — how many of them
#                run in the default `go test ./...` (no DSN) versus how many
#                actually execute against a real PostgreSQL.
#
# WHY THIS EXISTS (2026-10-21, R57 轮)
#   Measured on this repo: in the default run (no DSN), 573 tests were skipped
#   and 412 of them (72%) were skipped because no database was reachable, at
#   401 distinct skip sites. `go test ./...` reported "ok" for every one of
#   those packages. The gates that would have caught real defects were not
#   failing — they were not running.
#
#   The repo already has the right instrument: scripts/audit/run-integration-gate.sh
#   starts a disposable database, injects all 20 DSN variable names, and
#   refuses to report success when the suite merely skipped. What was missing
#   was a one-command way to (a) obtain a usable PostgreSQL and (b) run the
#   gate across every integration-gated package, with the no-DSN number next
#   to the with-DSN number.
#
#   Five things this script exists to not re-discover:
#     1. the Citus image tag. start-isolated-pg.sh pins kx-citus-pg17:offline-arm64,
#        which is not on the registry; the arm64 build ships as
#        registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-arm64 and has to be tagged.
#     2. the container name. The harness defaults to PG_CONTAINER=llm-gateway-pg;
#        start-isolated-pg.sh creates a container named kx-citus. They differ.
#     3. the port. start-isolated-pg.sh binds 15433, which is already taken on
#        some hosts by an unrelated stack.
#     4. the DSN names. The suite reads the database URL under 20 different
#        variable names; the harness injects all of them and that is the only
#        reason a真库 leg runs at all.
#     5. GATE_APPLY_STARTUP_TWICE. The re-runnability pass dies before the
#        tests run when the known-gaps manifest is stale, and it is a property
#        of the migration chain, not of the package under test — so it is
#        measured once, separately, not 33 times.
#
# Usage:
#   bash scripts/audit/measure-realdb-legs.sh --realdb        # with a real DB
#   bash scripts/audit/measure-realdb-legs.sh --no-dsn        # default run, for contrast
#   bash scripts/audit/measure-realdb-legs.sh --list           # package list only
#   bash scripts/audit/measure-realdb-legs.sh --realdb ./db    # one package
#
# Env:
#   PG_CONTAINER   container to use (default: llm-gateway-pg if present, else kx-citus)
#   PG_USER        database user (default: llm_gateway)
#   PG_PASSWORD    database password (default: read from the container's env)
#   OUT_DIR        where the per-package logs land (default: /tmp/realdb-legs)
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

MODE=""
PKG_FILTER=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --realdb) MODE=realdb ;;
    --no-dsn) MODE=nodsn ;;
    --list)   MODE=list ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) PKG_FILTER="$1" ;;
  esac
  shift
done
[[ -n "$MODE" ]] || { echo "用法：--realdb | --no-dsn | --list [包路径]" >&2; exit 2; }

OUT_DIR="${OUT_DIR:-/tmp/realdb-legs}"
mkdir -p "$OUT_DIR"

# ---- 包清单：与 harness 内部用的是同一套判据（只有 integration tag 才引入的文件）
derive_packages() {
  bash scripts/audit/derive-gate-packages.sh 2>/dev/null
}
if [[ "$MODE" == "list" ]]; then
  derive_packages
  exit 0
fi

# ---- 取数据库凭据：优先复用已在跑的本机容器，其次起一个一次性容器
pick_container() {
  if [[ -n "${PG_CONTAINER:-}" ]]; then echo "$PG_CONTAINER"; return; fi
  if docker ps --format '{{.Names}}' | grep -qx "llm-gateway-pg"; then
    echo "llm-gateway-pg"; return
  fi
  if docker ps --format '{{.Names}}' | grep -qx "kx-citus"; then echo "kx-citus"; return; fi
  echo ""
}
CONTAINER="$(pick_container)"
if [[ "$MODE" == "realdb" && -z "$CONTAINER" ]]; then
  echo "找不到可用的 PG 容器。先起一个（见下），或用 PG_CONTAINER 指定：" >&2
  echo "  docker tag registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-arm64 kx-citus-pg17:offline-arm64" >&2
  echo "  bash scripts/audit/start-isolated-pg.sh   # 端口 15433 若被占用就改 PG_PORT" >&2
  exit 2
fi

if [[ "$MODE" == "realdb" ]]; then
  export PG_CONTAINER="$CONTAINER"
  export PG_USER="${PG_USER:-llm_gateway}"
  if [[ -z "${PG_PASSWORD:-}" ]]; then
    PG_PASSWORD="$(docker inspect "$CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
                   | sed -n 's/^POSTGRES_PASSWORD=//p' | head -1)"
    export PG_PASSWORD
  fi
  [[ -n "$PG_PASSWORD" ]] || { echo "取不到 ${CONTAINER} 的 POSTGRES_PASSWORD，用 PG_PASSWORD 显式指定" >&2; exit 2; }
  # 第二遍重跑阶段与包无关（只重放迁移链），且它会在进入测试阶段前中止 ⇒
  # 本 sweep 关掉它；重跑阶段单独用 run-integration-gate.sh 的默认参数量。
  export GATE_APPLY_STARTUP_TWICE=0
  echo "== 真库测量 =="
  echo "   容器=$CONTAINER 用户=$PG_USER 输出=$OUT_DIR"
else
  echo "== 默认跑法（无 DSN）测量 =="
  echo "   ⚠ 这一路不注入任何 DSN ⇒ 真库腿会 skip ⇒ 这是「它们平时到底跑不跑」的读数"
fi

SUMMARY="$OUT_DIR/summary-$MODE.txt"
: > "$SUMMARY"
printf '%-42s %-8s %-6s %-6s %-6s %s\n' PACKAGE rc PASS SKIP FAIL DURATION >> "$SUMMARY"

while read -r pkg; do
  [[ -n "$pkg" ]] || continue
  if [[ -n "$PKG_FILTER" && "$pkg" != "$PKG_FILTER" ]]; then continue; fi
  safe="$(echo "$pkg" | sed 's#^./##; s#/#-#g')"
  log="$OUT_DIR/$safe.test.log"
  out="$OUT_DIR/$safe.out"
  start=$(date +%s)
  if [[ "$MODE" == "realdb" ]]; then
    GATE_LOG="$log" bash scripts/audit/run-integration-gate.sh "$pkg" > "$out" 2>&1
  else
    # 同一批包，但在**没有**任何 DSN 的环境里跑 —— 用来数「默认跑法下它们被 skip 掉多少」
    ( unset TEST_DATABASE_URL TEST_DB_URL TEST_PG_URL TEST_PG_DSN DATABASE_URL \
             LLM_GATEWAY_PG_URL TEST_AUDIT_ISOLATED_DB_URL TEST_TENANT_DATABASE_URL
      go test -tags=integration -count=1 -v -timeout 20m "$pkg" ) > "$log" 2>&1
  fi
  rc=$?
  dur=$(( $(date +%s) - start ))
  np=$(grep -cE '^\s*--- PASS' "$log" 2>/dev/null || echo 0)
  ns=$(grep -cE '^\s*--- SKIP' "$log" 2>/dev/null || echo 0)
  nf=$(grep -cE '^\s*--- FAIL' "$log" 2>/dev/null || echo 0)
  printf '%-42s %-8s %-6s %-6s %-6s %ss\n' "$pkg" "$rc" "$np" "$ns" "$nf" "$dur" | tee -a "$SUMMARY"
done < <(derive_packages)

echo
echo "== 汇总写入 $SUMMARY =="
cat "$SUMMARY"
echo
echo "★ 真库腿面怎么读："
echo "  --no-dsn 那份的 SKIP 列 ⇒ 默认跑法下这些断言**根本没跑**（go test 报的是 ok）"
echo "  --realdb 那份的 FAIL 列 ⇒ 真跑之后暴露的问题（含夹具形态不匹配与真缺陷，"
echo "    两者必须分开判：形态不匹配会报 'no rows'/'empty'/'already exists'，"
echo "    真缺陷会报具体不变量/数值不符）"
