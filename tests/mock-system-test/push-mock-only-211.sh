#!/usr/bin/env bash
# =====================================================================
# push-mock-only-211.sh — 把 mock-only 压测栈同步到 211 并可在远端执行
# =====================================================================
# 目标布局（211 上）：
#   /opt/lgw-mock-stack/repo/          精简源码树（构建网关镜像用，含 web/dist、vendor、sql）
#   /opt/lgw-mock-stack/repo/tests/mock-system-test/   compose + seed + init + loadtest + run 脚本
#
# 与生产部署的隔离：
#   * 只写 /opt/lgw-mock-stack/**，不触碰 /opt/llm-gateway-go-211、
#     /opt/kaixuan/data/llm-gateway、nginx、生产 PG/Redis。
#   * compose 项目名 lgw-mock-only、容器名 lgw-mock-only-*、网段 lgw_mock_only。
#   * 网关发布口默认 18781（211 上 8781/8783 是生产蓝绿槽）。
#
# 用法（在开发机）：
#   bash tests/mock-system-test/push-mock-only-211.sh sync     # 同步源码树
#   bash tests/mock-system-test/push-mock-only-211.sh up       # 同步 + 远端起栈 + 门禁
#   bash tests/mock-system-test/push-mock-only-211.sh load [--quick] [--scenarios ...]
#   bash tests/mock-system-test/push-mock-only-211.sh gate|smoke|status
#   bash tests/mock-system-test/push-mock-only-211.sh destroy
# =====================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REMOTE="${MOCK_STACK_SSH:-211}"
STACK_DIR="${MOCK_STACK_DIR:-/opt/lgw-mock-stack}"

rsync_repo() {
  say "rsyncing pruned repo → ${REMOTE}:${STACK_DIR}/repo ..."
  ssh "$REMOTE" "mkdir -p '$STACK_DIR/repo'"
  rsync -az --delete \
    --exclude '.git' \
    --exclude 'node_modules' \
    --exclude '/docs' \
    --exclude '/ml-training' \
    --exclude '.build-local' \
    --exclude '/logs' \
    --exclude '/run' \
    --exclude '/bin' \
    --exclude '/gateway' \
    --exclude '/gateway-v2' \
    --exclude '/quality-monitor' \
    --exclude '/web-mobile' \
    --exclude '/backups' \
    --exclude '.env' --exclude '.env.*' \
    --exclude '._*' --exclude '.DS_Store' \
    --exclude '*.log' \
    "$ROOT/" "$REMOTE:$STACK_DIR/repo/"
  [[ -f "$ROOT/web/dist/index.html" ]] || die "web/dist/index.html missing — build the SPA first"
  say "synced."
}

remote_run() {
  ssh "$REMOTE" "cd '$STACK_DIR/repo' && bash tests/mock-system-test/run-mock-only.sh $*"
}

say()  { printf '[push-211] %s\n' "$*"; }
die()  { printf '[push-211] FATAL: %s\n' "$*" >&2; exit 1; }

case "${1:-status}" in
  sync)    rsync_repo ;;
  up)      rsync_repo; remote_run up ;;
  gate)    remote_run gate ;;
  smoke)   remote_run smoke ;;
  load)    shift; remote_run load "$@" ;;
  status)  remote_run status ;;
  down)    remote_run down ;;
  destroy) remote_run destroy ;;
  *)
    sed -n '2,26p' "$0"; exit 1 ;;
esac
