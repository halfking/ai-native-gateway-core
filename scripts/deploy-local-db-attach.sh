#!/usr/bin/env bash
# =============================================================================
# deploy-local-db-attach.sh — PG17 就绪后的数据库自动接线（供定时任务调用）
#
# 职责（幂等，可反复运行）：
#   1. Docker/PG17 容器未就绪 → 打印原因并退出 0（等下一次定时触发）
#   2. 就绪 → 跑 deploy-local-pg17-docker.sh（建容器/建库）
#          → 重跑 deploy-local-sys.sh deploy --no-frontend（sync_db_url 回填
#            DATABASE_URL、应用 schema/迁移、重启 gateway）
#          → verify /readyz 出现 database
#   3. 成功后写标记文件，后续调用直接退出（防重复）
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_ROOT="${LLM_GATEWAY_SYS_INSTALL_ROOT:-C:\\llm-gateway-go}"
# Git Bash 下把 Windows 路径（C:\llm-gateway-go）转为 /c/... 形式，混合分隔符会坑 coreutils
if command -v cygpath >/dev/null 2>&1; then
  INSTALL_ROOT_UNIX="$(cygpath -u "$INSTALL_ROOT" 2>/dev/null || printf '%s' "$INSTALL_ROOT")"
else
  INSTALL_ROOT_UNIX="$INSTALL_ROOT"
fi
MARKER="$HOME/.llm-gateway-go/db-attach.done"
LOG_DIR="${INSTALL_ROOT_UNIX}/logs"
# 防定时重叠锁（R29 审计）：deploy-local-sys.sh 内部 go build 可跑数分钟，
# 旧版标记文件成功后才写，两次定时触发可完全重叠。mkdir 原子，第二实例
# 直接退出 0 等下次触发；deploy-local-sys.sh 另有部署锁，不会递归抢锁。
LOCK_DIR="$HOME/.llm-gateway-go/db-attach.lock"

log() { printf '[db-attach %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[[ -f "$MARKER" ]] && { log "已完成（标记 $MARKER 存在），跳过"; exit 0; }

mkdir -p "$(dirname "$MARKER")"
# 陈锁自愈（R30 审计）：kill -9 等致死信号不走 EXIT trap，残留锁会让定时
# 任务此后永远 exit 0 静默跳过、DB 接线无限期搁置且无告警。合法运行最长
# 面 = pg17 脚本 + 完整 deploy（含 go build）+ 150s readyz 等待 ≈ 10 分钟级，
# 30 分钟阈值在其之上；锁目录 mtime 超龄即视为死实例残留，清理后继续。
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  if [[ -d "$LOCK_DIR" ]] && find "$LOCK_DIR" -maxdepth 0 -mmin +30 >/dev/null 2>&1 \
     && [[ -n "$(find "$LOCK_DIR" -maxdepth 0 -mmin +30 2>/dev/null)" ]]; then
    log "检测到陈锁（mtime 超过 30 分钟，holder_pid=$(cat "$LOCK_DIR/pid" 2>/dev/null || echo '?')），判定为死实例残留，清理后继续"
    rm -rf "$LOCK_DIR"
    mkdir "$LOCK_DIR"
  else
    log "另一 db-attach 实例正在运行（holder_pid=$(cat "$LOCK_DIR/pid" 2>/dev/null || echo '?')），本次跳过"
    exit 0
  fi
fi
printf '%s' "$$" > "$LOCK_DIR/pid"
# INT/TERM 先转成非零退出，让 EXIT trap 统一清锁（kill -9 仍无解，由上面的
# 陈锁自愈兜底）。
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'rc=$?; rm -rf "$LOCK_DIR"; exit $rc' EXIT

mkdir -p "$LOG_DIR"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  log "Docker 未就绪（等待另外的任务完成安装）；下次定时再试"
  exit 0
fi
log "Docker 已就绪: $(docker --version)"

log "安装/确认 PostgreSQL 17 容器..."
bash "$SCRIPT_DIR/deploy-local-pg17-docker.sh" 2>&1 | tee -a "$LOG_DIR/db-attach-pg17.log"

log "重跑 deploy 完成数据库接线（schema + 迁移 + DATABASE_URL 回填 + 重启）..."
bash "$SCRIPT_DIR/deploy-local-sys.sh" deploy --no-frontend 2>&1 | tee -a "$LOG_DIR/db-attach-deploy.log"

port="$(grep -o 'LLM_GATEWAY_LISTEN=":[0-9]*' "${INSTALL_ROOT_UNIX}/config/gateway.env" | grep -o '[0-9]*$')"
# 成功判定：/readyz 中 database 字段存在且不为 null（旧实现 grep '"database"'
# 会把未接库时的 "database":null 误判为成功）
for i in $(seq 1 30); do
  readyz="$(curl -fsS --max-time 5 "http://127.0.0.1:${port}/readyz" 2>/dev/null || true)"
  if [[ -n "$readyz" ]] && printf '%s' "$readyz" | grep -q '"database"' \
     && ! printf '%s' "$readyz" | grep -q '"database":null'; then
    log "数据库已接入：/readyz = $readyz"
    date -u +%Y-%m-%dT%H:%M:%SZ > "$MARKER"
    log "接线完成，写入标记 $MARKER"
    exit 0
  fi
  [[ $i == 1 ]] && log "等待 gateway 以 DB 模式就绪（/readyz: ${readyz:-不可达}）..."
  sleep 5
done
log "FATAL: gateway 重启后 150s 内 /readyz 未报告 database，请人工检查 $LOG_DIR/db-attach-deploy.log"
exit 1
