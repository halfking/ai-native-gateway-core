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

# === 单实例闸门（2026-10-10 生产事故修复）================================
# 起因：2026-10-10 252 现场，pg17-disk-watch.sh 堆到 310 份副本、149 个
# `docker exec ... psql` 僵死，把 15G 内存吃到 swap 100% / load 300，
# 反而把 PG 压到无法服务 154 网关（SQLSTATE 57P03、一小时 194 个 503）。
# 根因是本目录**所有**巡检都缺同一道闸门：PG 半死（容器还在 listen、
# 事务卡死）时 psql 会一直挂着，而 cron 的下一次触发照常发生 ——
# 进程只增不减。⇒ 每 10 分钟一跑的巡检，在最需要它的时刻（PG 出问题）
# 恰恰是最不能多跑的时刻。
#
# ★ 为什么 inline 而不是 source 一个公共 helper：
#   本目录按**整文件覆盖**部署到 /opt/scripts/（见 etc.cron.d.pg17 头部
#   「漏一条 = 删一条」）。helper 没跟着传上去，source 失败会让**整个目录**
#   的巡检同时哑掉。本目录已经吃过一次这种耦合的亏，不再引入第二次。
#
# ★ 拿不到锁时**放行**（fail-open），不拒绝执行：
#   闸门不可用时让监控整体静默，比重复采样更糟 —— 静默的监控不会被任何人
#   发现已经坏了。下次采样行会记 gate=FAIL_OPEN，让 fail-open 本身可见。
#
# ★ 抢不到锁时退出 0 而不是 3：本轮确实没有新采样，但巡检**没有失效**
#   （上一轮正在产出结论）。3 在本目录契约里是「量具不可用」，报它会
#   触发一次假的「巡检失效」告警。
LLMGW_LOCK_DIR=${LLMGW_LOCK_DIR:-/var/run/lock}
_llmgw_lock_file="$LLMGW_LOCK_DIR/llmgw-$(basename "$0" .sh).lock"

_llmgw_single_instance_guarded=false
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && exec 9>"$_llmgw_lock_file" 2>/dev/null; then
  if ! flock -n 9; then
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: $(basename "$0") 上一轮仍在运行（lock=${_llmgw_lock_file}），本轮不重复执行" \
      >> "${LLMGW_LOCK_SKIP_LOG:-/var/log/llmgw-single-instance-skip.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

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
