#!/usr/bin/env bash
# clean-logs.sh — 请求日志定时清理（由 client-deploy.sh 的 cron/launchd 任务调用）。
#
# 网关的 request_logs 表随请求量增长，长期会膨胀磁盘。本脚本通过 PG 直接
# 删除超过保留天数的冷数据（表结构与列名由网关 ApplyMigrations 保证）。
# 失败必须退出非 0。
#
# Env:
#   INSTALL_ROOT       默认 /opt/llm-gateway
#   PG_CONTAINER       PG 容器名（默认 llm-gateway-pg）
#   POSTGRES_USER      PG 用户（默认 llm_user）
#   POSTGRES_DB        库名（默认 llm_gateway）
#   POSTGRES_PASSWORD   PG 密码（优先从 .env 读取）
#   LOG_RETENTION_DAYS  保留天数（默认 30）
#   DRY_RUN            1=只打印 DELETE 行数预估，不实际删除
set -euo pipefail

INSTALL_ROOT="${INSTALL_ROOT:-/opt/llm-gateway}"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
POSTGRES_USER="${POSTGRES_USER:-llm_user}"
POSTGRES_DB="${POSTGRES_DB:-llm_gateway}"
LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS:-30}"
DRY_RUN="${DRY_RUN:-0}"
ENV_FILE="${INSTALL_ROOT}/.env"

log() { echo "[clean-logs] $*"; }
die() { echo "[clean-logs] ERROR: $*" >&2; exit 1; }

if [[ -z "${POSTGRES_PASSWORD:-}" && -f "$ENV_FILE" ]]; then
  POSTGRES_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
[[ -n "${POSTGRES_PASSWORD:-}" ]] || die "POSTGRES_PASSWORD 未设置"

psql_exec() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1"
}

# 网关请求日志表名：优先 request_logs（网关 ApplyMigrations 的热表），
# 不存在则探测 request_logs_hot（部分部署使用冷热分表）。
LOG_TABLE="$(psql_exec "SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('request_logs','request_logs_hot') LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"
[[ -n "$LOG_TABLE" ]] || { log "未找到 request_logs 表，跳过清理（首次部署或表名不同）"; exit 0; }

# 时间列：created_at（网关标准列）；缺失则跳过避免误删。
COL="$(psql_exec "SELECT column_name FROM information_schema.columns WHERE table_name='${LOG_TABLE}' AND column_name IN ('created_at','requested_at','created_time') LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"
[[ -n "$COL" ]] || { log "${LOG_TABLE} 无可识别的时间列，跳过清理"; exit 0; }

cutoff="$(date -u -d "${LOG_RETENTION_DAYS} days ago" +%Y-%m-%dT00:00:00Z 2>/dev/null \
  || date -u -v-${LOG_RETENTION_DAYS}d +%Y-%m-%dT00:00:00Z 2>/dev/null \
  || echo "")"
[[ -n "$cutoff" ]] || die "无法计算截止日期（date 不支持 -d/-v）"

count_before="$(psql_exec "SELECT count(*) FROM ${LOG_TABLE} WHERE ${COL} < '${cutoff}'" 2>/dev/null || echo 0)"
log "${LOG_TABLE}.${COL} < ${cutoff}：待清理 ${count_before} 行"

if [[ "$DRY_RUN" == "1" ]]; then
  log "DRY_RUN=1 — skip DELETE"
  exit 0
fi

if [[ "$count_before" -gt 0 ]] 2>/dev/null; then
  # 分批删除（每批 5000）避免长事务锁表；网关在跑时也能继续写。
  deleted=0
  while :; do
    n="$(psql_exec "WITH del AS (DELETE FROM ${LOG_TABLE} WHERE ctid IN (SELECT ctid FROM ${LOG_TABLE} WHERE ${COL} < '${cutoff}' LIMIT 5000) RETURNING 1) SELECT count(*) FROM del" 2>/dev/null || echo 0)"
    n="${n:-0}"
    [[ "$n" -gt 0 ]] 2>/dev/null || break
    deleted=$((deleted + n))
    log "已删除 ${deleted} 行（本批 ${n}）"
  done
  log "清理完成：共删除 ${deleted} 行"
else
  log "无需清理"
fi
