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
LOG=/var/log/pg17-drop-old-columnar-partitions.log
RETAIN_MONTHS=2

# 所有带 _YYYY_MM 月分区的列存/heap 父表
#（requiring hot-table + monthly partition architecture）
PARENTS=(
  model_probe_runs
  _request_logs
  _request_logs_bodies
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
