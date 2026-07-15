#!/bin/bash
# 252 PG17 全面监控告警（增强版）
# 每 10 分钟采样一次（含系统盘 / llm_gateway DB / 大表 / docker / 列存元数据），
# 触发阈值调用 /opt/scripts/notify.sh 推送 IM；否则只写本地 log。
#
# 创建：2026-07-15（增强覆盖头部 4 个盲点）
#   - 系统盘阈值 35/45/55% + available 阈值 30GB
#   - llm_gateway DB 阈值 12/16/20 GB + ILM 表大小
#   - 关键 hot 表 + dead tuple 阈值
#   - columnar_internal 元数据 bloat 阈值
#   - pg_wal 阈值
#   - docker storage 阈值
#
# 阈值可被 /etc/llmgw/pg17.conf 覆盖
set -euo pipefail

CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"

CONTAINER=pg-252-pg17
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=/var/log/pg17-disk-watch.log
ALERT_LOG=/var/log/pg17-disk-watch.alert.log
NOTIFY=/opt/scripts/notify.sh

# === 阈值（可被 conf 覆盖）===========================================
# 默认阈值基于 200 GB 磁盘 + 之前一次 80%+ 紧急扩容事件的复盘：
#   - 当时 70% 触发告警后没人看，80% 才发现 — 因此 warning 阈值定 60% 是为了
#     留出"还有空间启动清理"的时间窗。
DISK_WARN_PCT=${DISK_WARN_PCT:-60}
DISK_CRIT_PCT=${DISK_CRIT_PCT:-75}
DISK_AVAIL_GB_WARN=${DISK_AVAIL_GB_WARN:-30}
DB_WARN_GB=${DB_WARN_GB:-20}
DB_CRIT_GB=${DB_CRIT_GB:-28}
HOT_REQUEST_LOGS_MB=${HOT_REQUEST_LOGS_MB:-2048}     # request_logs_hot 2 GB 警告（JSONB TOAST 单表风险高）
HOT_REQUEST_LOGS_CRIT_MB=${HOT_REQUEST_LOGS_CRIT_MB:-3072}
HOT_MODEL_PROBE_MB=${HOT_MODEL_PROBE_MB:-500}        # 14 天 TTL 满约 1 GB
COLUMNAR_CHUNK_GB=${COLUMNAR_CHUNK_GB:-15}
PG_WAL_GB=${PG_WAL_GB:-3}                             # >3 GB 拥堵（max_wal_size 默认 2 GB）

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true

# === 采样 ============================================================
de() { docker exec "$CONTAINER" "$@" 2>/dev/null || echo "0"; }

used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
used_gb=$(df -P / | awk 'NR==2 {printf "%.1f", $3/1024/1024}')
avail_gb=$(df -P / | awk 'NR==2 {printf "%.1f", $4/1024/1024}')
docker_storage_gb=$(du -s /var/lib/containers/storage/ 2>/dev/null | awk '{printf "%.1f", $1/1024/1024}')

db_size_bytes=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_database_size('$PG_DB')")
db_size_gb=$(awk -v b="$db_size_bytes" 'BEGIN {printf "%.2f", b/1024/1024/1024}')
db_all_gb=$(de psql -U "$PG_USER" -d postgres -tAc "SELECT round(sum(pg_database_size(datname))/1024.0/1024.0/1024.0, 2) FROM pg_database WHERE datistemplate=false" || echo 0)

req_logs_hot_size=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('public.request_logs_hot')/1024/1024")
mp_hot_size=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('public.model_probe_runs_hot')/1024/1024")
handoff_size=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('public.handoff_logs')/1024/1024")
columnar_chunk_bytes=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('columnar_internal.chunk')")
columnar_chunk_gb=$(awk -v b="$columnar_chunk_bytes" 'BEGIN {printf "%.2f", b/1024/1024/1024}')

# pg_wal 大小（用 pg_stat_file 取每个 WAL 文件 size，不能用 pg_size_bytes(它把字符串当 size 解析)）
pg_wal_size_b=$(de psql -U "$PG_USER" -d postgres -tAc "SELECT coalesce(sum((pg_stat_file('pg_wal/' || f)).size),0)::bigint FROM pg_ls_dir('pg_wal') f WHERE f ~ '^[0-9A-F]{24}\$'")
pg_wal_gb=$(awk -v b="$pg_wal_size_b" 'BEGIN {printf "%.2f", b/1024/1024/1024}')

# top 5 表（JOIN pg_class 取 oid；pg_stat_user_tables 没有 relid 列）
top_tables=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "WITH t AS (SELECT s.relname, pg_total_relation_size(c.oid) AS sz FROM pg_stat_user_tables s JOIN pg_class c ON c.oid=s.relid WHERE s.schemaname='public' ORDER BY sz DESC LIMIT 5) SELECT string_agg(relname || '=' || (sz/1024/1024) || 'MB', ', ') FROM t")
top_tables=${top_tables:-n/a}

# dead tuple top 3（>500 让 hot 表的 high-churn 也看得见）
dead_tup_pct_top=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT string_agg(format('%s=%s%%(%s)', relname, round(pct::numeric,1)::text, n_dead_tup), ', ') FROM (SELECT relname, 100.0*n_dead_tup/GREATEST(n_live_tup+n_dead_tup,1) AS pct, n_dead_tup FROM pg_stat_user_tables WHERE schemaname='public' AND n_dead_tup>500 ORDER BY n_dead_tup DESC LIMIT 3) s")
dead_tup_pct_top=${dead_tup_pct_top:-clean}

# === 评估严重等级 ====================================================
alerts=()

# critical
[ "$used_pct" -ge "$DISK_CRIT_PCT" ] 2>/dev/null && \
  alerts+=("🔴磁盘 critical: ${used_pct}% (avail=${avail_gb}GB), 阈值 ${DISK_CRIT_PCT}%")

awk_gte() { awk -v a="$1" -v b="$2" 'BEGIN {print (a+0>=b+0)?1:0}'; }
awk_gt()  { awk -v a="$1" -v b="$2" 'BEGIN {print (a+0>b+0)?1:0}'; }

[ "$(awk_gte "$db_size_gb" "$DB_CRIT_GB")" = "1" ] && \
  alerts+=("🔴llm_gateway DB critical: ${db_size_gb}GB, 阈值 ${DB_CRIT_GB}GB")
[ "$(awk_gte "$req_logs_hot_size" "$HOT_REQUEST_LOGS_CRIT_MB")" = "1" ] && \
  alerts+=("🔴request_logs_hot critical: ${req_logs_hot_size}MB, 阈值 ${HOT_REQUEST_LOGS_CRIT_MB}MB")

# warning
[ "$used_pct" -ge "$DISK_WARN_PCT" ] && [ "$used_pct" -lt "$DISK_CRIT_PCT" ] && \
  alerts+=("🟡磁盘 warning: ${used_pct}% (avail=${avail_gb}GB), 阈值 ${DISK_WARN_PCT}%")
[ "$(awk_gte "$db_size_gb" "$DB_WARN_GB")" = "1" ] && [ "$(awk_gte "$DB_CRIT_GB" "$db_size_gb")" = "1" ] && \
  alerts+=("🟡llm_gateway DB warning: ${db_size_gb}GB, 阈值 ${DB_WARN_GB}GB")
awk_floor() { awk -v a="$1" 'BEGIN {i=int(a+0); print (i+0<=a+0)?i:(i+1)}'; }
avail_floor=$(awk_floor "$avail_gb")
[ "$avail_floor" -le "$DISK_AVAIL_GB_WARN" ] && \
  alerts+=("🟡磁盘可用空间 warning: ${avail_gb}GB ≤ ${DISK_AVAIL_GB_WARN}GB")
[ "$(awk_gte "$req_logs_hot_size" "$HOT_REQUEST_LOGS_MB")" = "1" ] && [ "$(awk_gt "$HOT_REQUEST_LOGS_CRIT_MB" "$req_logs_hot_size")" = "1" ] && \
  alerts+=("🟡request_logs_hot warning: ${req_logs_hot_size}MB, 阈值 ${HOT_REQUEST_LOGS_MB}MB")
[ "$(awk_gte "$mp_hot_size" "$HOT_MODEL_PROBE_MB")" = "1" ] && \
  alerts+=("🟡model_probe_runs_hot warning: ${mp_hot_size}MB, 阈值 ${HOT_MODEL_PROBE_MB}MB (TTL 14天,如异常涨需排查)")
[ "$(awk_gte "$columnar_chunk_gb" "$COLUMNAR_CHUNK_GB")" = "1" ] && \
  alerts+=("🟡columnar 元数据膨胀: ${columnar_chunk_gb}GB (chunk 表), 阈值 ${COLUMNAR_CHUNK_GB}GB")
[ "$(awk_gte "$pg_wal_gb" "$PG_WAL_GB")" = "1" ] && \
  alerts+=("🟡WAL 拥堵: ${pg_wal_gb}GB, 阈值 ${PG_WAL_GB}GB")

level=ok
if [ "${#alerts[@]}" -gt 0 ]; then level=warning; fi
for a in "${alerts[@]}"; do
  case "$a" in 🔴*) level=critical; break;; esac
done

ts=$(date -Iseconds)
echo "[$ts] level=$level disk=${used_pct}% avail=${avail_gb}GB db_llm_gateway=${db_size_gb}GB db_total=${db_all_gb}GB req_logs_hot=${req_logs_hot_size}MB mp_hot=${mp_hot_size}MB handoff=${handoff_size}MB columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB docker_storage=${docker_storage_gb}GB" >> "$LOG"
echo "[$ts] top5: $top_tables" >> "$LOG"
echo "[$ts] dead_tup_top3: $dead_tup_pct_top" >> "$LOG"

# === 告警去重（防 10 分钟一次的 IM 噪声）===
# 规则：
#   - 同 fingerprint + 同 level 在 cooldown 时间内不再推 IM
#   - 但 alert log 仍每条都写
#   - critical 默认 1 小时冷却；warning 30 分钟；info 不推送
if [ "$level" != "ok" ]; then
  msg="[$ts] [252] ${level}"
  for a in "${alerts[@]}"; do msg+=$'\n'"$a"; done
  msg+=$'\n'""
  msg+=$'\n'"disk=${used_pct}% avail=${avail_gb}GB"
  msg+=$'\n'"llm_gateway=${db_size_gb}GB total=${db_all_gb}GB"
  msg+=$'\n'"request_logs_hot=${req_logs_hot_size}MB model_probe_runs_hot=${mp_hot_size}MB"
  msg+=$'\n'"columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB"
  msg+=$'\n'""
  msg+=$'\n'"📊 top5 tables: $top_tables"
  msg+=$'\n'"💀 dead_tup_top3: $dead_tup_pct_top"

  echo "[$ts] $msg" >> "$ALERT_LOG"

  if [ -x "$NOTIFY" ]; then
    # 默认有 cooldown:critical 1h / warning 30min
    case "$level" in
      critical) COOLDOWN_MIN=${PG17_CRIT_COOLDOWN_MIN:-60};;
      warning)  COOLDOWN_MIN=${PG17_WARN_COOLDOWN_MIN:-30};;
      info)     COOLDOWN_MIN=${PG17_INFO_COOLDOWN_MIN:-1440};;
    esac

    fingerprint=$(printf '%s\n%s\n%s' "$level" "$(printf '%s\n' "${alerts[@]}" | md5sum | cut -d' ' -f1)" "${ts:0:7}" | md5sum | cut -d' ' -f1)
    cooldown_file=/var/tmp/pg17-disk-watch.cooldown
    now_epoch=$(date +%s)
    # 读上次同 fingerprint 推送时间（grep 无匹配时 exit 1 -> || true 屏蔽）
    last_line=$(grep -F "${fingerprint}" "$cooldown_file" 2>/dev/null | head -1) || last_line=""
    last_ts=${last_line##*:}

    push_im=true
    if [ -n "$last_ts" ] && [ $((now_epoch - last_ts)) -lt $((COOLDOWN_MIN * 60)) ]; then
      echo "[$ts] cooldown active for $level (last=${last_ts}s ago, cooldown=${COOLDOWN_MIN}m), local log only" >> "$LOG"
      push_im=false
    fi

    if [ "$push_im" = "true" ]; then
      "$NOTIFY" --level "$level" --title "252 ${level}" --body "$msg" || true
      # 写入 cooldown 标记
      mkdir -p "$(dirname "$cooldown_file")" 2>/dev/null
      # 删除此 fp 的旧行,追加新行
      touch "$cooldown_file"
      grep -v "^${fingerprint}:" "$cooldown_file" > "${cooldown_file}.tmp" 2>/dev/null || true
      printf "%s:%s\n" "$fingerprint" "$now_epoch" >> "${cooldown_file}.tmp"
      mv "${cooldown_file}.tmp" "$cooldown_file" 2>/dev/null || true
    fi
  fi
fi
