#!/bin/bash
# 252 PG17 bloat 周级清理（VACUUM FULL 防患于未然）
#
# 每 7 天（周日凌晨 03:15）跑。查找表 bloat 超过阈值的大表执行 VACUUM FULL，
# 释放死元组归还物理页（autovacuum 不会归还已分配页）。
#
# 阈值：bloat_pct >= 30% AND size >= 256MB 才处理。
# VACUUM FULL 期间会 ACCESS EXCLUSIVE 锁表，所以限制单表时长 30 分钟，
# 同时错开 pg17-drop-old-columnar-partitions.sh（每月 1 号 02:30）。
#
# 创建：2026-07-15（伴随 252 磁盘满事件）
set -euo pipefail

CONTAINER=pg-252-pg17
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=/var/log/pg17-vacuum-bloat.log
TABLE_TIMEOUT_MIN=30   # 单表 VACUUM FULL 上限（分钟）

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
echo "[$(date -Iseconds)] start vacuum-bloat weekly cleanup" >> "$LOG"

# === 列存元数据表（最需要 VACUUM FULL，DROP 列存表后必有大量 dead tuple）===
META_TABLES=("chunk" "stripe" "chunk_group")
for meta in "${META_TABLES[@]}"; do
  size=$(docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo "0")
  echo "[$(date -Iseconds)]   columnar_internal.${meta} size=${size}MB - start VACUUM FULL" >> "$LOG"
  # 2026-10-03 修复：原写法把 SET 与 VACUUM FULL 放在同一个 -c 里，psql 会把它们
  # 包进一个隐式事务块，VACUUM 恒报 "cannot run inside a transaction block" ——
  # 自 2026-07-15 上线起每一个周日 100% 失败（已核对 /var/log/pg17-vacuum-bloat.log）。
  # 退出码还被 `|| echo FAILED` 吞掉，结尾照打 done，所以长期无人察觉。
  # 正确写法：超时参数走 PGOPTIONS，VACUUM FULL 单独一条语句下发。
  docker exec -e PGOPTIONS="-c statement_timeout=${TABLE_TIMEOUT_MIN}min -c lock_timeout=5min" \
    "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
    -c "VACUUM FULL columnar_internal.${meta}" >> "$LOG" 2>&1 \
    || echo "[$(date -Iseconds)]   VACUUM FULL columnar_internal.${meta} FAILED" >> "$LOG"
  new_size=$(docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo "0")
  echo "[$(date -Iseconds)]   columnar_internal.${meta} size=${size}MB -> ${new_size}MB" >> "$LOG"
done

# === 业务表 bloat 检测 ===
# 用 pgstattuple 扩展（如果没有就用 dead_tup_pct 估算）
BLOAT_SQL="
WITH ranked AS (
  SELECT
    schemaname||'.'||relname AS tname,
    pg_total_relation_size(relid)::bigint AS size,
    n_live_tup, n_dead_tup,
    CASE WHEN n_live_tup + n_dead_tup > 0
         THEN 100.0 * n_dead_tup / (n_live_tup + n_dead_tup)
         ELSE 0 END AS dead_pct
  FROM pg_stat_user_tables
  WHERE schemaname NOT IN ('columnar_internal')
)
SELECT tname, size, round(dead_pct::numeric, 1)
FROM ranked
WHERE dead_pct >= 30 AND size > 256*1024*1024
ORDER BY dead_pct DESC, size DESC
LIMIT 20;
"

TARGETS=$(docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$BLOAT_SQL" 2>/dev/null || echo "")
if [ -z "$TARGETS" ]; then
  echo "[$(date -Iseconds)] no business tables need VACUUM FULL (no bloat > 30% with size >= 256MB)" >> "$LOG"
else
  echo "[$(date -Iseconds)] bloat targets:" >> "$LOG"
  echo "$TARGETS" >> "$LOG"
  while IFS='|' read -r tname size dead_pct; do
    [ -z "$tname" ] && continue
    echo "[$(date -Iseconds)]   VACUUM FULL ${tname} (size=${size} bytes, dead_pct=${dead_pct}%)" >> "$LOG"
    # 2026-10-03 同上修复（事务块问题）
    docker exec -e PGOPTIONS="-c statement_timeout=${TABLE_TIMEOUT_MIN}min -c lock_timeout=5min" \
      "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX \
      -c "VACUUM FULL ${tname}" >> "$LOG" 2>&1 \
      || echo "[$(date -Iseconds)]   VACUUM FULL ${tname} FAILED" >> "$LOG"
  done <<< "$TARGETS"
fi

echo "[$(date -Iseconds)] done" >> "$LOG"
