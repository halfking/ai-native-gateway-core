#!/usr/bin/env bash
# backup-db.sh — PG 数据库定时备份（由 client-deploy.sh 的 cron/launchd 任务调用）。
#
# 通过 docker exec 进入 pg 容器执行 pg_dump，落盘到 $INSTALL_ROOT/data/backups/，
# 保留最近 DB_BACKUP_KEEP 份。失败必须退出非 0，否则 cron 会把失败当成功吞掉。
#
# Env:
#   INSTALL_ROOT       默认 /opt/llm-gateway
#   PG_CONTAINER       PG 容器名（默认 llm-gateway-pg）
#   POSTGRES_USER      PG 用户（默认 llm_user）
#   POSTGRES_DB        备份库名（默认 llm_gateway）
#   POSTGRES_PASSWORD   PG 密码（优先从 $INSTALL_ROOT/.env 读取）
#   DB_BACKUP_KEEP     保留份数（默认 7）
#   DRY_RUN            1=只打印
set -euo pipefail

INSTALL_ROOT="${INSTALL_ROOT:-/opt/llm-gateway}"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
POSTGRES_USER="${POSTGRES_USER:-llm_user}"
POSTGRES_DB="${POSTGRES_DB:-llm_gateway}"
DB_BACKUP_KEEP="${DB_BACKUP_KEEP:-7}"
DRY_RUN="${DRY_RUN:-0}"
ENV_FILE="${INSTALL_ROOT}/.env"
BACKUP_DIR="${INSTALL_ROOT}/data/backups"

log() { echo "[backup-db] $*"; }
die() { echo "[backup-db] ERROR: $*" >&2; exit 1; }

# 从 .env 读密码（cron 环境不带交互式变量）。
if [[ -z "${POSTGRES_PASSWORD:-}" && -f "$ENV_FILE" ]]; then
  POSTGRES_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
[[ -n "${POSTGRES_PASSWORD:-}" ]] || die "POSTGRES_PASSWORD 未设置（亦不在 ${ENV_FILE}）"

mkdir -p "$BACKUP_DIR"
stamp="$(date +%Y%m%d-%H%M%S)"
out="${BACKUP_DIR}/${POSTGRES_DB}-${stamp}.dump"

log "pg_dump → ${out}"
if [[ "$DRY_RUN" == "1" ]]; then
  log "DRY_RUN=1 — skip"
  exit 0
fi

# custom-format dump：支持并行恢复 + 单文件，比 plain SQL 更适合备份轮转。
if ! docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
  pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/_bkup.dump; then
  die "pg_dump 失败（容器 $PG_CONTAINER）"
fi
docker cp "${PG_CONTAINER}:/tmp/_bkup.dump" "$out"
docker exec "$PG_CONTAINER" rm -f /tmp/_bkup.dump
log "备份完成: ${out} ($(du -h "$out" | cut -f1))"

# 轮转：保留最新 DB_BACKUP_KEEP 份。
# 逐行读入而不是 mapfile：mapfile 是 bash 4.0+，macOS 自带 /bin/bash 是 3.2。
files=()
while IFS= read -r _ln; do files+=("$_ln"); done < <(ls -1 "${BACKUP_DIR}"/${POSTGRES_DB}-*.dump 2>/dev/null | sort)
total=${#files[@]}
if [[ "$total" -gt "$DB_BACKUP_KEEP" ]]; then
  remove=$((total - DB_BACKUP_KEEP))
  for f in "${files[@]:0:remove}"; do
    rm -f "$f"
    log "轮转删除: $(basename "$f")"
  done
fi
log "done（保留 ${DB_BACKUP_KEEP} 份）"
