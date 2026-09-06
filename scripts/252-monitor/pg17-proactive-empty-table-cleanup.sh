#!/bin/bash
# 252 PG17 预防性空表清理（每日定时执行）
#
# 目的：每日主动清理空表，避免累积到触发紧急清理的程度。
# 与 pg17-emergency-cleanup.sh 的区别：
#   - emergency: 磁盘 >= 90% 时被动触发，执行 L2（含 VACUUM FULL）
#   - proactive:  每日主动执行，只做 L1（DROP 空表），零风险
#
# 执行频率：每日 02:00（与热表迁移任务同步，清理迁移后产生的空表）
# cron: 0 2 * * * root llmgw-source /opt/scripts/pg17-proactive-empty-table-cleanup.sh
#
# 安全特性：
#   1. 双重验证：n_live_tup=0 AND n_dead_tup=0 AND COUNT(*)=0
#   2. 预检查：磁盘 < 85%、无 VACUUM FULL 运行、24h cooldown
#   3. Dry-run 模式：--dry-run 只列出不执行
#   4. 详细日志：/var/log/pg17-proactive-cleanup.log
#   5. 飞书告警：成功/警告/失败都推送
#
# 创建：2026-09-06（伴随 252 磁盘 89% → 55% 清理事件）
set -euo pipefail

CONTAINER=pg-252-pg17
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=/var/log/pg17-proactive-cleanup.log
NOTIFY=/opt/scripts/notify.sh

# 配置（可被环境变量或 /etc/llmgw/pg17.conf 覆盖）
CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"

DISK_THRESHOLD_PCT=${PROACTIVE_DISK_THRESHOLD_PCT:-85}  # 磁盘 >= 85% 时跳过
MIN_TABLE_SIZE_MB=${PROACTIVE_MIN_TABLE_SIZE_MB:-100}   # 只清理 >= 100MB 的表
COOLDOWN_HOURS=${PROACTIVE_COOLDOWN_HOURS:-24}          # 24 小时内只执行一次
COOLDOWN_FILE=/var/tmp/pg17-proactive-cleanup.cooldown

DRY_RUN=false
FORCE=false

# 解析参数
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=true; shift;;
    --force)   FORCE=true; shift;;
    *) echo "[error] unknown arg: $1" >&2; exit 2;;
  esac
done

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
ts=$(date -Iseconds)
echo "[$ts] ========== proactive empty table cleanup start ==========" >> "$LOG"
echo "[$ts] DRY_RUN=$DRY_RUN FORCE=$FORCE" >> "$LOG"

docker_exec() { docker exec "$CONTAINER" "$@" 2>/dev/null; }

# === 预检查 1: cooldown ===
if [ "$FORCE" = "false" ] && [ -f "$COOLDOWN_FILE" ]; then
  last_run=$(cat "$COOLDOWN_FILE" 2>/dev/null || echo "0")
  now_epoch=$(date +%s)
  elapsed_hours=$(( (now_epoch - last_run) / 3600 ))
  if [ "$elapsed_hours" -lt "$COOLDOWN_HOURS" ]; then
    echo "[$ts] SKIP: cooldown active (last run ${elapsed_hours}h ago, threshold ${COOLDOWN_HOURS}h)" >> "$LOG"
    exit 0
  fi
fi

# === 预检查 2: 磁盘使用率 ===
used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$ts] disk usage: ${used_pct}%" >> "$LOG"
if [ "$used_pct" -ge "$DISK_THRESHOLD_PCT" ]; then
  echo "[$ts] SKIP: disk usage ${used_pct}% >= ${DISK_THRESHOLD_PCT}% (let emergency-cleanup handle it)" >> "$LOG"
  [ -x "$NOTIFY" ] && "$NOTIFY" -l warning -t "252 预防性清理跳过" -b "磁盘使用率 ${used_pct}% >= ${DISK_THRESHOLD_PCT}%，由紧急清理接管" || true
  exit 0
fi

# === 预检查 3: 是否有正在运行的 VACUUM FULL ===
running_vacuum=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT COUNT(*) FROM pg_stat_activity WHERE query ILIKE '%VACUUM FULL%' AND state = 'active'" || echo "0")
if [ "$running_vacuum" -gt 0 ]; then
  echo "[$ts] SKIP: VACUUM FULL is running (avoid lock conflict)" >> "$LOG"
  exit 0
fi

disk_before_gb=$(df -P / | awk 'NR==2 {printf "%.1f", ($3/1024/1024)}')
db_before_gb=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT round(pg_database_size('$PG_DB')/1024.0/1024.0/1024.0, 2)" || echo "0")

echo "[$ts] pre-cleanup: disk=${disk_before_gb}GB used, db=${db_before_gb}GB" >> "$LOG"

# === 查找空表（n_live_tup=0 AND n_dead_tup=0 AND size >= 100MB）===
EMPTY_TABLES_SQL="
SELECT 
  format('%I.%I', s.schemaname, s.relname) AS tname,
  pg_total_relation_size(s.relid)/1024/1024 AS size_mb,
  s.n_live_tup,
  s.n_dead_tup
FROM pg_stat_user_tables s
WHERE s.schemaname NOT IN ('columnar_internal', 'pg_catalog', 'information_schema')
  AND s.relname NOT LIKE '%_hot'
  AND s.n_live_tup = 0
  AND s.n_dead_tup = 0
  AND pg_total_relation_size(s.relid) >= ${MIN_TABLE_SIZE_MB}*1024*1024
ORDER BY pg_total_relation_size(s.relid) DESC;
"

EMPTY_TABLES=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tA -F'|' -c "$EMPTY_TABLES_SQL" || echo "")

if [ -z "$EMPTY_TABLES" ]; then
  echo "[$ts] no empty tables found (n_live=0 AND n_dead=0 AND size >= ${MIN_TABLE_SIZE_MB}MB)" >> "$LOG"
  exit 0
fi

echo "[$ts] found empty tables:" >> "$LOG"
echo "$EMPTY_TABLES" >> "$LOG"

# === 查找默认分区（需要额外 COUNT(*) 验证）===
DEFAULT_PARTS_SQL="
SELECT 
  child.relname AS child_name,
  parent.relname AS parent_name
FROM pg_inherits i
JOIN pg_class child  ON child.oid  = i.inhrelid
JOIN pg_class parent ON parent.oid = i.inhparent
WHERE child.relname LIKE '%_default'
  AND parent.relname IN (
    'request_logs','usage_ledger','request_wal','tool_usage_stats',
    'routing_decision_log','credit_ledger','credential_model_index',
    'handoff_logs','candidate_failure_logs','credential_model_call_history'
  )
  AND pg_total_relation_size(child.oid) >= ${MIN_TABLE_SIZE_MB}*1024*1024;
"

DEFAULT_PARTS=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tA -F'|' -c "$DEFAULT_PARTS_SQL" || echo "")

dropped_count=0
skipped_nonempty_count=0
skipped_nonempty_list=""

# === 处理空表 ===
while IFS='|' read -r tname size_mb n_live n_dead; do
  [ -z "$tname" ] && continue
  
  # 额外验证：执行 COUNT(*) 确认真的为 0 行
  actual_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM ${tname}" 2>/dev/null || echo "-1")
  
  if [ "$actual_count" != "0" ]; then
    echo "[$ts] SKIP ${tname}: n_live_tup=0 but COUNT(*)=${actual_count} (stats may be stale)" >> "$LOG"
    skipped_nonempty_count=$((skipped_nonempty_count + 1))
    skipped_nonempty_list+="${tname} (${actual_count} rows), "
    continue
  fi
  
  if [ "$DRY_RUN" = "true" ]; then
    echo "[$ts] [DRY-RUN] would DROP ${tname} (${size_mb}MB, ${actual_count} rows)" >> "$LOG"
    dropped_count=$((dropped_count + 1))
  else
    echo "[$ts] DROP TABLE ${tname} (${size_mb}MB, verified ${actual_count} rows)" >> "$LOG"
    if docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "DROP TABLE IF EXISTS ${tname}" >> "$LOG" 2>&1; then
      echo "[$ts]   → OK" >> "$LOG"
      dropped_count=$((dropped_count + 1))
    else
      echo "[$ts]   → FAILED" >> "$LOG"
    fi
  fi
done <<< "$EMPTY_TABLES"

# === 处理默认分区（额外 COUNT(*) 验证）===
while IFS='|' read -r child_name parent_name; do
  [ -z "$child_name" ] && continue
  
  # 验证是否真的为 0 行
  actual_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM ${child_name}" 2>/dev/null || echo "-1")
  
  if [ "$actual_count" != "0" ]; then
    echo "[$ts] SKIP default partition ${child_name}: has ${actual_count} rows (NOT EMPTY!)" >> "$LOG"
    skipped_nonempty_count=$((skipped_nonempty_count + 1))
    skipped_nonempty_list+="${child_name} (${actual_count} rows), "
    continue
  fi
  
  if [ "$DRY_RUN" = "true" ]; then
    echo "[$ts] [DRY-RUN] would DETACH + DROP ${child_name} from ${parent_name}" >> "$LOG"
    dropped_count=$((dropped_count + 1))
  else
    echo "[$ts] DETACH + DROP default partition ${child_name} from ${parent_name}" >> "$LOG"
    if docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
        "ALTER TABLE ${parent_name} DETACH PARTITION ${child_name}; DROP TABLE IF EXISTS ${child_name};" >> "$LOG" 2>&1; then
      echo "[$ts]   → OK" >> "$LOG"
      dropped_count=$((dropped_count + 1))
    else
      echo "[$ts]   → FAILED" >> "$LOG"
    fi
  fi
done <<< "$DEFAULT_PARTS"

disk_after_gb=$(df -P / | awk 'NR==2 {printf "%.1f", ($3/1024/1024)}')
db_after_gb=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT round(pg_database_size('$PG_DB')/1024.0/1024.0/1024.0, 2)" || echo "0")
disk_saved_gb=$(awk -v before="$disk_before_gb" -v after="$disk_after_gb" 'BEGIN {printf "%.1f", before - after}')
db_saved_gb=$(awk -v before="$db_before_gb" -v after="$db_after_gb" 'BEGIN {printf "%.2f", before - after}')

echo "[$ts] post-cleanup: disk=${disk_after_gb}GB used, db=${db_after_gb}GB" >> "$LOG"
echo "[$ts] saved: disk=${disk_saved_gb}GB, db=${db_saved_gb}GB" >> "$LOG"
echo "[$ts] dropped=${dropped_count} tables, skipped=${skipped_nonempty_count} non-empty" >> "$LOG"
echo "[$ts] ========== done ==========" >> "$LOG"

# === 更新 cooldown ===
if [ "$DRY_RUN" = "false" ]; then
  date +%s > "$COOLDOWN_FILE"
fi

# === 推送告警 ===
if [ -x "$NOTIFY" ] && [ "$DRY_RUN" = "false" ]; then
  msg="[252] 预防性空表清理完成

✅ 删除表: ${dropped_count}
⚠️ 跳过非空: ${skipped_nonempty_count}
💾 回收空间: 磁盘 ${disk_saved_gb}GB, 数据库 ${db_saved_gb}GB

磁盘: ${disk_before_gb}GB → ${disk_after_gb}GB (${used_pct}%)
数据库: ${db_before_gb}GB → ${db_after_gb}GB"

  if [ "$skipped_nonempty_count" -gt 0 ]; then
    msg+="

⚠️ 发现非空的「空表」(统计信息可能过期):
${skipped_nonempty_list%%, }"
    "$NOTIFY" -l warning -t "252 预防性清理" -b "$msg" || true
  elif [ "$dropped_count" -gt 0 ]; then
    "$NOTIFY" -l info -t "252 预防性清理" -b "$msg" || true
  fi
fi

exit 0
