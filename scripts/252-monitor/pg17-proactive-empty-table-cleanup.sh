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
# 可注入：让本脚本的行为门能用一个假 `docker` 跑到真实判定分支，
# 而不是只能对源码做子串断言（那量的是「写过这句话」，不是「行为变了」）。
DOCKER_BIN=${DOCKER_BIN:-docker}
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=${LOG:-/var/log/pg17-proactive-cleanup.log}
NOTIFY=/opt/scripts/notify.sh

# 配置（可被环境变量或 /etc/llmgw/pg17.conf 覆盖）
CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"

DISK_THRESHOLD_PCT=${PROACTIVE_DISK_THRESHOLD_PCT:-85}  # 磁盘 >= 85% 时跳过
MIN_TABLE_SIZE_MB=${PROACTIVE_MIN_TABLE_SIZE_MB:-100}   # 只清理 >= 100MB 的表
COOLDOWN_HOURS=${PROACTIVE_COOLDOWN_HOURS:-24}          # 24 小时内只执行一次
COOLDOWN_FILE=${COOLDOWN_FILE:-/var/tmp/pg17-proactive-cleanup.cooldown}

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
echo "[$(date -Iseconds)] ========== proactive empty table cleanup start ==========" >> "$LOG"
echo "[$(date -Iseconds)] DRY_RUN=$DRY_RUN FORCE=$FORCE" >> "$LOG"

docker_exec() { "$DOCKER_BIN" exec "$CONTAINER" "$@" 2>/dev/null; }

# === 预检查 1: cooldown ===
if [ "$FORCE" = "false" ] && [ -f "$COOLDOWN_FILE" ]; then
  last_run=$(cat "$COOLDOWN_FILE" 2>/dev/null || echo "0")
  now_epoch=$(date +%s)
  elapsed_hours=$(( (now_epoch - last_run) / 3600 ))
  if [ "$elapsed_hours" -lt "$COOLDOWN_HOURS" ]; then
    echo "[$(date -Iseconds)] SKIP: cooldown active (last run ${elapsed_hours}h ago, threshold ${COOLDOWN_HOURS}h)" >> "$LOG"
    exit 0
  fi
fi

# === 预检查 2: 磁盘使用率 ===
used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$(date -Iseconds)] disk usage: ${used_pct}%" >> "$LOG"
if [ "$used_pct" -ge "$DISK_THRESHOLD_PCT" ]; then
  echo "[$(date -Iseconds)] SKIP: disk usage ${used_pct}% >= ${DISK_THRESHOLD_PCT}% (let emergency-cleanup handle it)" >> "$LOG"
  [ -x "$NOTIFY" ] && "$NOTIFY" -l warning -t "252 预防性清理跳过" -b "磁盘使用率 ${used_pct}% >= ${DISK_THRESHOLD_PCT}%，由紧急清理接管" || true
  exit 0
fi

# === 预检查 3: 是否有正在运行的 VACUUM FULL ===
running_vacuum=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT COUNT(*) FROM pg_stat_activity WHERE query ILIKE '%VACUUM FULL%' AND state = 'active'" || echo "0")
if [ "$running_vacuum" -gt 0 ]; then
  echo "[$(date -Iseconds)] SKIP: VACUUM FULL is running (avoid lock conflict)" >> "$LOG"
  exit 0
fi

disk_before_gb=$(df -P / | awk 'NR==2 {printf "%.1f", ($3/1024/1024)}')
db_before_gb=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT round(pg_database_size('$PG_DB')/1024.0/1024.0/1024.0, 2)" || echo "0")

echo "[$(date -Iseconds)] pre-cleanup: disk=${disk_before_gb}GB used, db=${db_before_gb}GB" >> "$LOG"

# === 查找空表（n_live_tup=0 AND n_dead_tup=0 AND size >= 100MB）===
#
# ★ 2026-10-06 修订（审计 §10.74）：**必须排除分区**，理由两条，各自独立成立。
#
# ① DEFAULT 分区是父表写入路径的组成部分。丢掉它，父表就再也接不住
#    「落在所有显式边界之外」的行 —— 后续 INSERT 直接报错。
#    实测（154 直连生产）：现存 **13 个空的 DEFAULT 分区**，其中
#    `usage_facts_default` 已达 **79.6 MB**，距本脚本 100 MB 阈值只差 20.4 MB。
#    它膨胀的来源正是 §10.73.2 那类「清空后索引不回收」。
#    ★ 而该父表 `usage_facts` **不在**下方 DEFAULT_PARTS_SQL 的 10 表白名单里，
#    所以它不会被第 2 段（DETACH+DROP）处理，而是被**本段**捞出来裸 DROP。
#
# ② 任何分区都不该由本脚本 DROP。分区的生命周期归 `bg/partition_manager.go`
#    与 `domains/ursm/v2/persist/retention_partition.go`（DROP 型留存）所有；
#    旁路 DROP 会让那两处的分区账本失同步。本脚本的职责是「清空**表**」。
#
# 两道独立的闸：`NOT c.relispartition`（主，治结构）与 `NOT LIKE '%_default'`
# （兜，治 DEFAULT 被错误编目）。不是冗余，是各自覆盖不同的失效方式。
EMPTY_TABLES_SQL="
SELECT 
  format('%I.%I', s.schemaname, s.relname) AS tname,
  pg_total_relation_size(s.relid)/1024/1024 AS size_mb,
  s.n_live_tup,
  s.n_dead_tup,
  -- ★ 把「是不是分区」带出到 shell：让下面的循环能做**行为层**的独立判断。
  --   SQL 过滤与循环判断是两道不同的闸，各自覆盖不同失效方式；
  --   只做 SQL 过滤的话，将来有人改了那条 WHERE，本脚本就会重新开始裸 DROP 分区。
  c.relispartition::int AS is_partition
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
WHERE s.schemaname NOT IN ('columnar_internal', 'pg_catalog', 'information_schema')
  AND s.relname NOT LIKE '%_hot'
  AND NOT c.relispartition
  AND s.relname NOT LIKE '%_default'
  AND s.n_live_tup = 0
  AND s.n_dead_tup = 0
  AND pg_total_relation_size(s.relid) >= ${MIN_TABLE_SIZE_MB}*1024*1024
ORDER BY pg_total_relation_size(s.relid) DESC;
"

EMPTY_TABLES=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tA -F'|' -c "$EMPTY_TABLES_SQL" || echo "")

if [ -z "$EMPTY_TABLES" ]; then
  echo "[$(date -Iseconds)] no empty tables found (n_live=0 AND n_dead=0 AND size >= ${MIN_TABLE_SIZE_MB}MB)" >> "$LOG"
  exit 0
fi

echo "[$(date -Iseconds)] found empty tables:" >> "$LOG"
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
skipped_partition_count=0
skipped_nonempty_list=""

# === 处理空表 ===
while IFS='|' read -r tname size_mb n_live n_dead is_part; do
  [ -z "$tname" ] && continue

  # === 结构闸：分区一律不由本脚本 DROP（第二道闸，见 EMPTY_TABLES_SQL 上方注释）===
  # DEFAULT 分区额外用名字兜一道：万一 relispartition 被误编目，
  # 掉 DEFAULT 分区会让父表写不进越界行，代价远高于多留一张空表。
  case "$tname" in
    *_default) is_part=1 ;;
  esac
  if [ "$is_part" = "1" ]; then
    echo "[$(date -Iseconds)] SKIP ${tname}: 是分区，分区生命周期归 partition_manager / DROP 型留存所有，本脚本只清空表" >> "$LOG"
    skipped_partition_count=$((skipped_partition_count + 1))
    continue
  fi

  # 额外验证：执行 COUNT(*) 确认真的为 0 行
  actual_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM ${tname}" 2>/dev/null || echo "-1")
  
  if [ "$actual_count" != "0" ]; then
    echo "[$(date -Iseconds)] SKIP ${tname}: n_live_tup=0 but COUNT(*)=${actual_count} (stats may be stale)" >> "$LOG"
    skipped_nonempty_count=$((skipped_nonempty_count + 1))
    skipped_nonempty_list+="${tname} (${actual_count} rows), "
    continue
  fi
  
  if [ "$DRY_RUN" = "true" ]; then
    echo "[$(date -Iseconds)] [DRY-RUN] would DROP ${tname} (${size_mb}MB, ${actual_count} rows)" >> "$LOG"
    dropped_count=$((dropped_count + 1))
  else
    echo "[$(date -Iseconds)] DROP TABLE ${tname} (${size_mb}MB, verified ${actual_count} rows)" >> "$LOG"
    if docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "DROP TABLE IF EXISTS ${tname}" >> "$LOG" 2>&1; then
      echo "[$(date -Iseconds)]   → OK" >> "$LOG"
      dropped_count=$((dropped_count + 1))
    else
      echo "[$(date -Iseconds)]   → FAILED" >> "$LOG"
    fi
  fi
done <<< "$EMPTY_TABLES"

# === 处理默认分区（额外 COUNT(*) 验证）===
while IFS='|' read -r child_name parent_name; do
  [ -z "$child_name" ] && continue
  
  # 验证是否真的为 0 行
  actual_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM ${child_name}" 2>/dev/null || echo "-1")
  
  if [ "$actual_count" != "0" ]; then
    echo "[$(date -Iseconds)] SKIP default partition ${child_name}: has ${actual_count} rows (NOT EMPTY!)" >> "$LOG"
    skipped_nonempty_count=$((skipped_nonempty_count + 1))
    skipped_nonempty_list+="${child_name} (${actual_count} rows), "
    continue
  fi
  
  if [ "$DRY_RUN" = "true" ]; then
    echo "[$(date -Iseconds)] [DRY-RUN] would DETACH + DROP ${child_name} from ${parent_name}" >> "$LOG"
    dropped_count=$((dropped_count + 1))
  else
    echo "[$(date -Iseconds)] DETACH + DROP default partition ${child_name} from ${parent_name}" >> "$LOG"
    if docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
        "ALTER TABLE ${parent_name} DETACH PARTITION ${child_name}; DROP TABLE IF EXISTS ${child_name};" >> "$LOG" 2>&1; then
      echo "[$(date -Iseconds)]   → OK" >> "$LOG"
      dropped_count=$((dropped_count + 1))
    else
      echo "[$(date -Iseconds)]   → FAILED" >> "$LOG"
    fi
  fi
done <<< "$DEFAULT_PARTS"

disk_after_gb=$(df -P / | awk 'NR==2 {printf "%.1f", ($3/1024/1024)}')
db_after_gb=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT round(pg_database_size('$PG_DB')/1024.0/1024.0/1024.0, 2)" || echo "0")
disk_saved_gb=$(awk -v before="$disk_before_gb" -v after="$disk_after_gb" 'BEGIN {printf "%.1f", before - after}')
db_saved_gb=$(awk -v before="$db_before_gb" -v after="$db_after_gb" 'BEGIN {printf "%.2f", before - after}')

echo "[$(date -Iseconds)] post-cleanup: disk=${disk_after_gb}GB used, db=${db_after_gb}GB" >> "$LOG"
echo "[$(date -Iseconds)] saved: disk=${disk_saved_gb}GB, db=${db_saved_gb}GB" >> "$LOG"
echo "[$(date -Iseconds)] dropped=${dropped_count} tables, skipped=${skipped_nonempty_count} non-empty, skipped=${skipped_partition_count} partitions" >> "$LOG"
echo "[$(date -Iseconds)] ========== done ==========" >> "$LOG"

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
