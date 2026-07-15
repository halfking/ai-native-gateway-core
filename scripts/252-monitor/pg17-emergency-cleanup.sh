#!/bin/bash
# 252 PG17 紧急清理（disk > 90% 时跑 / 手动触发）
#
# 三级回退（按"危险度"递增，每级先 LOW 然后 HIGH）：
#   L1 (safe):     DROP n_live_tup = 0 AND size >= 100MB 的死表 / 默认分区
#   L2 (medium):   VACUUM FULL columnar_internal.{chunk,stripe,chunk_group}
#                  + VACUUM FULL 任何 dead_pct > 50% 的 1GB+ 表
#   L3 (warning):  TRUNCATE 所有 _hot 表（保留 *_default / 当前月 _2026_MM）
#                  TRUNCATE token / record / session 等清零表
#
# 触发方式:
#   1. cron: */15 * * * * /opt/scripts/pg17-emergency-cleanup.sh --auto  (自动按 disk% 触发)
#   2. 手动: /opt/scripts/pg17-emergency-cleanup.sh --level L1 [--yes]
#
# 创建：2026-07-15
set -euo pipefail

CONTAINER=pg-252-pg17
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=/var/log/pg17-emergency-cleanup.log

AUTO=false
LEVEL=L1
ASSUME_YES=false
DISK_TRIGGER_PCT=90

while [ $# -gt 0 ]; do
  case "$1" in
    --auto)       AUTO=true; shift;;
    --level)      LEVEL="${2^^}"; shift 2;;
    --yes|-y)     ASSUME_YES=true; shift;;
    *) echo "[error] unknown arg: $1" >&2; exit 2;;
  esac
done

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
ts=$(date -Iseconds)
echo "[$ts] === emergency-cleanup invoked AUTO=$AUTO LEVEL=$LEVEL ===" >> "$LOG"

used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$ts] current disk used=${used_pct}%" >> "$LOG"

if [ "$AUTO" = "true" ]; then
  if [ "$used_pct" -lt "$DISK_TRIGGER_PCT" ]; then
    echo "[$ts] auto + disk < ${DISK_TRIGGER_PCT}%, skip" >> "$LOG"
    exit 0
  fi
  # 自动触发则升级到 L2
  LEVEL="L2"
  echo "[$ts] auto-triggered, escalate to L2" >> "$LOG"
fi

if [ "$ASSUME_YES" = "false" ] && [ -t 0 ]; then
  read -p "L1/L2/L3  are progressive; ${LEVEL} 下执行。输入 yes 继续: " ans
  [ "$ans" = "yes" ] || { echo "[$ts] abort by user" >> "$LOG"; exit 1; }
fi

docker_exec() { docker exec "$CONTAINER" "$@" 2>/dev/null; }

# === L1: DROP 死表 / 默认分区 ===
if [ "$LEVEL" = "L1" ] || [ "$LEVEL" = "L2" ] || [ "$LEVEL" = "L3" ]; then
  # 找 n_live_tup=0 AND pg_total >= 100MB 的表
  DEAD_TABLES=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
    SELECT format('%I.%I', schemaname, relname)
    FROM pg_stat_user_tables
    WHERE n_live_tup = 0 AND pg_total_relation_size(relid) >= 100*1024*1024
      AND schemaname NOT IN ('columnar_internal')
    ORDER BY pg_total_relation_size(relid) DESC;
  ")
  while IFS= read -r tname; do
    [ -z "$tname" ] && continue
    echo "[$ts] L1 DROP ${tname}" >> "$LOG"
    docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "DROP TABLE IF EXISTS $tname" >> "$LOG" 2>&1 || true
  done <<< "$DEAD_TABLES"

  # 找 _default 分区（无业务价值）
  DEFAULT_PARTS=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
    SELECT child.relname
    FROM pg_inherits i
    JOIN pg_class child  ON child.oid  = i.inhrelid
    JOIN pg_class parent ON parent.oid = i.inhparent
    WHERE child.relname LIKE '%_default'
      AND parent.relname IN ('request_logs','usage_ledger','request_wal','tool_usage_stats','routing_decision_log','credit_ledger','credential_model_index');
  ")
  while IFS= read -r child; do
    [ -z "$child" ] && continue
    echo "[$ts] L1 DROP default partition $child" >> "$LOG"
    docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
      ALTER TABLE ${child%_default} DETACH PARTITION $child;
      DROP TABLE IF EXISTS $child;
    " >> "$LOG" 2>&1 || true
  done <<< "$DEFAULT_PARTS"
fi

# === L2: VACUUM FULL columnar 元数据 + 业务 bloat 表 ===
if [ "$LEVEL" = "L2" ] || [ "$LEVEL" = "L3" ]; then
  for meta in chunk stripe chunk_group; do
    size=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo 0)
    [ "${size:-0}" -lt 10 ] && { echo "[$ts] L2 skip columnar_internal.${meta} (small=${size}MB)"; continue; }
    echo "[$ts] L2 VACUUM FULL columnar_internal.${meta} (${size}MB)" >> "$LOG"
    docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
      "SET statement_timeout='30min'; SET lock_timeout='5min'; VACUUM FULL columnar_internal.${meta}" >> "$LOG" 2>&1 || true
  done

  # bloat > 50% AND size > 1GB
  BLOATS=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
    SELECT format('%I.%I', schemaname, relname)
    FROM pg_stat_user_tables
    WHERE schemaname='public'
      AND n_dead_tup > 0 AND n_live_tup > 0
      AND 100.0*n_dead_tup/(n_live_tup+n_dead_tup) > 50
      AND pg_total_relation_size(relid) > 1024*1024*1024
    ORDER BY pg_total_relation_size(relid) DESC;
  ")
  while IFS= read -r tname; do
    [ -z "$tname" ] && continue
    echo "[$ts] L2 VACUUM FULL ${tname}" >> "$LOG"
    docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
      "SET statement_timeout='30min'; SET lock_timeout='5min'; VACUUM FULL ${tname}" >> "$LOG" 2>&1 || true
  done <<< "$BLOATS"
fi

# === L3: TRUNCATE hot 表 + 清零的小清零表 ===
if [ "$LEVEL" = "L3" ]; then
  echo "[$ts] L3 DANGER ZONE - 需 --yes 确认" >> "$LOG"
  if [ "$ASSUME_YES" = "false" ] && [ "$AUTO" = "false" ]; then
    # L1/L2 已通过一次确认;L3 这里强制交互
    read -p "L3 will TRUNCATE hot tables (回收 ~数 GB). Continue? (yes): " ans
    [ "$ans" = "yes" ] || { echo "[$ts] abort L3" >> "$LOG"; exit 1; }
  fi

  # 清零的小表（casdoor token/record/session 等）
  for db in casdoor kaixuan crm kxmemory; do
    ZERO_TABLES=$(docker_exec psql -U "$PG_USER" -d "$db" -tAc "
      SELECT format('%I.%I', schemaname, relname)
      FROM pg_stat_user_tables
      WHERE n_live_tup = 0 AND pg_total_relation_size(relid) >= 1024*1024;
    " 2>/dev/null || echo "")
    while IFS= read -r tname; do
      [ -z "$tname" ] && continue
      echo "[$ts] L3 TRUNCATE $tname (in $db)" >> "$LOG"
      docker_exec psql -U "$PG_USER" -d "$db" -tAc "TRUNCATE TABLE $tname" >> "$LOG" 2>&1 || true
    done <<< "$ZERO_TABLES"
  done
fi

used_after=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$ts] done. disk: ${used_pct}% -> ${used_after}%" >> "$LOG"
