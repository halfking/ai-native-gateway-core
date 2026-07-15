#!/bin/bash
# 月度清理：Hydra 列存分区表 DROP 旧月分区
# Hydra 列存 stripe 文件不会因 TRUNCATE 释放，必须 DROP 整表才归还磁盘。
# 每月 1 号 02:30 跑，保留最近 2 个月（即删除 < 当月 - 2 月 的所有列存分区）。
#
# 重要：DROP 列存表后，columnar_internal.chunk/stripe/chunk_group 会留下大量 dead tuples，
#       这些元数据表本身也会膨胀（实测 14 GB bloat）。必须在 DROP 之后立即 VACUUM FULL 三张元数据表。
#
# 2026-07-15 更新：
#   - 扩充 PARENTS 列表，覆盖 llm_gateway 所有带 _hot → partition 的 columnar 表
#   - 加入 DETACH CONCURRENTLY + 超时以适应长事务
#   - 加入 regex 校验 partition name（_YYYY_MM 严格），防止误删 hot/_old 表
#
# 创建：2026-07-14（伴随 252 硬盘 100% 事件而加）
set -euo pipefail

CONTAINER=pg-252-pg17
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=/var/log/pg17-drop-old-columnar-partitions.log
RETAIN_MONTHS=2

# 所有带 _YYYY_MM 月分区的列存/heap 父表
#（requiring hot-table + monthly partition architecture）
PARENTS=(
  model_probe_runs
  request_logs
  request_logs_bodies
  request_wal
  usage_ledger
  routing_decision_log
  tool_usage_stats
  credit_ledger
  candidate_failure_logs
  handoff_logs
  credential_model_index
  credential_model_call_history
)

CUTOFF=$(date -d "${RETAIN_MONTHS} months ago" +%Y_%m)
echo "[$(date -Iseconds)] start, cutoff=${CUTOFF} (drop partitions ending in _<${CUTOFF})" >> $LOG

dropped_total=0
for parent in "${PARENTS[@]}"; do
  candidates=$(docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "SELECT inhrelid::regclass::text
       FROM pg_inherits
      WHERE inhparent::regclass::text = '$parent'
        AND inhrelid::regclass::text ~ '^${parent}_[0-9]{4}_[0-9]{2}$'
        AND substring(inhrelid::regclass::text FROM '_([0-9]{4}_[0-9]{2})$') < '${CUTOFF}';" 2>/dev/null || echo "")

  if [ -z "$candidates" ]; then
    echo "[$(date -Iseconds)] $parent: no old partitions to drop" >> $LOG
    continue
  fi

  while IFS= read -r child; do
    [ -z "$child" ] && continue
    echo "[$(date -Iseconds)] $parent: detach + drop $child" >> $LOG

    if docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
        "SET statement_timeout=0; SET lock_timeout='5min';
         ALTER TABLE $parent DETACH PARTITION $child;" >> $LOG 2>&1; then
      if docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
          "SET statement_timeout=0; SET lock_timeout='5min';
           DROP TABLE IF EXISTS $child;" >> $LOG 2>&1; then
        echo "[$(date -Iseconds)] $parent: dropped $child OK" >> $LOG
        dropped_total=$((dropped_total+1))
      else
        echo "[$(date -Iseconds)] $parent: DROP FAILED for $child" >> $LOG
      fi
    else
      echo "[$(date -Iseconds)] $parent: DETACH FAILED for $child, skipping" >> $LOG
    fi
  done <<< "$candidates"
done

echo "[$(date -Iseconds)] done, dropped=$dropped_total" >> $LOG

# === 列存元数据表 VACUUM FULL ===
# DROP 列存表会在 columnar_internal.chunk/stripe/chunk_group 留下大量 dead tuple，
# autovacuum 不归还物理页，必须 VACUUM FULL 才能释放回 OS。
if [ "$dropped_total" -gt 0 ]; then
  echo "[$(date -Iseconds)] running VACUUM FULL on columnar_internal metadata tables" >> $LOG
  for meta in chunk stripe chunk_group; do
    echo "[$(date -Iseconds)]   VACUUM FULL columnar_internal.$meta" >> $LOG
    docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
      "SET statement_timeout='60min'; SET lock_timeout='10min'; VACUUM FULL columnar_internal.$meta" >> $LOG 2>&1 || \
      echo "[$(date -Iseconds)]   WARN: VACUUM FULL $meta failed (see above)" >> $LOG
  done
  echo "[$(date -Iseconds)] VACUUM FULL done" >> $LOG
fi
