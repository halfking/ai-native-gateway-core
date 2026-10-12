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

# === 单实例闸门（2026-10-10 生产事故修复）================================
# 起因是一次真实的服务降级，不是预防性加固。2026-10-10 15:49 现场：
#   · 252 上本脚本有 **310 份副本**同时在跑，最早一份已挂了 6.7 小时；
#     149 个 `docker exec pg-252-pg17 psql` 卡住不退。
#   · 成因是「PG 半死」：容器还在 listen，但事务卡死。本脚本的 psql 既没有
#     statement_timeout 也没有 connect_timeout，于是**一直挂着**；
#     cron 每 10 分钟照常再拉一份，进程只增不减。
#   · 每份副本还会跑一次 `du -s /var/lib/containers/storage/`（全量遍历
#     容器存储），310 份 = IO 风暴 + 内存耗尽：252 的 15G 内存用满、
#     swap 100% 占满、load 300。
#   · 结局是本末倒置：**巡检把被巡检对象压垮了** —— PG 无法服务，
#     154 网关持续拿到 SQLSTATE 57P03，一小时 194 个 HTTP 503。
#
# 所以这道闸门不是"洁癖"：没有它，本脚本在 PG 最需要被观察的时候，
# 恰恰是最不能多跑的时候。
#
# ★ 为什么 inline 而不是 source 一个公共 helper：
#   本目录的部署方式是**整文件覆盖**到 /opt/scripts/（见 etc.cron.d.pg17
#   头部：「漏一条 = 删一条」）。新增一个必须同批上传的 helper，等于给每个
#   脚本加一条部署耦合 —— helper 没传上去，source 失败会让**整个目录**的
#   巡检同时哑掉。本目录已经因为这种耦合出过一次事故，不再引入第二次。
#
# ★ 拿不到锁时**放行**（fail-open）而不是拒绝执行：闸门不可用时让监控
#   整体静默，比重复采样更糟 —— 静默的监控不会被任何人发现已经坏了。
LLMGW_LOCK_DIR=${LLMGW_LOCK_DIR:-/var/run/lock}
_llmgw_lock_key=$(basename "$0" .sh)
_llmgw_lock_file="$LLMGW_LOCK_DIR/llmgw-${_llmgw_lock_key}.lock"

_llmgw_single_instance_guarded=false
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && exec 9>"$_llmgw_lock_file" 2>/dev/null; then
  if ! flock -n 9; then
    # 上一轮还在跑 —— 这是**正常路径**：本轮没有新采样，但巡检并没有失效
    # （上一轮正在产出）。故退出 0 而不是 3：
    #   3 在本目录契约里是「量具不可用 / 本次没有结论」，会触发
    #   「巡检失效」告警；而这里恰恰有人正在给出结论，报失效是假警报。
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: 上一轮 pg17-disk-watch 仍在运行（lock=${_llmgw_lock_file}），本轮不重复采样" \
      >> "${LLMGW_DISK_WATCH_LOG:-/var/log/pg17-disk-watch.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

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
# ★ 每个外部调用都必须有上界（2026-10-10 事故的第二道闸）。
#   第一道闸（单实例）保证「最多一份副本」，这一道保证「一份副本也一定会结束」。
#   两者缺一不可：只有单实例而没有超时，那一份副本能挂到天荒地老，
#   巡检从此变成一个永久性的僵尸；两者都在，才同时有「不堆积」和「会退出」。
#
# 分两层：
#   1) PG 侧 —— statement_timeout 让**查询本身**停下来
#      （否则卡在锁等待/IO 上，连接建成了也拿不到结果）；
#   2) 客户端侧 —— docker exec 整体上界。容器半死时 exec 本身也会挂住，
#      这一层是事故当天 149 个僵死 `docker exec` 的直接对应物。
LLMGW_QUERY_TIMEOUT=${LLMGW_QUERY_TIMEOUT:-15}
LLMGW_CONNECT_TIMEOUT=${LLMGW_CONNECT_TIMEOUT:-5}
LLMGW_QUERY_TIMEOUT_MS=${LLMGW_QUERY_TIMEOUT_MS:-$((LLMGW_QUERY_TIMEOUT * 1000))}

# 优先用 coreutils timeout（Linux 上有，能真正回收整棵进程树）；
# 没有时退化为纯 bash 看门狗。开发机（macOS）两者都没有，但那里跑的是门，
# 不是生产 —— 纯 bash 分支保证门仍然可执行，不会被这里卡死。
_llmgw_run_bounded() {
  local secs="$1"; shift
  if command -v timeout >/dev/null 2>&1; then
    timeout -k 5 "$secs" "$@"
    return $?
  fi
  "$@" &
  local pid=$! waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$secs" ]; then
      kill -9 "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

# PGOPTIONS 走服务端超时，exec 外的 timeout 走客户端超时。两者不可互相替代：
# 只设客户端的话，连接会按时断开但**查询仍占着 PG 的进程槽**直到服务端自己放弃。
de() {
  _llmgw_run_bounded "$LLMGW_QUERY_TIMEOUT" \
    docker exec \
      -e "PGOPTIONS=-c statement_timeout=${LLMGW_QUERY_TIMEOUT_MS} -c lock_timeout=5000" \
      -e "PGCONNECT_TIMEOUT=$LLMGW_CONNECT_TIMEOUT" \
      "$CONTAINER" "$@" 2>/dev/null || echo "0"
}

used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
used_gb=$(df -P / | awk 'NR==2 {printf "%.1f", $3/1024/1024}')
avail_gb=$(df -P / | awk 'NR==2 {printf "%.1f", $4/1024/1024}')
# ★ `du -s` 全量遍历容器存储目录，是本脚本里最贵的一个动作：
#   事故当天现场的 du 进程集体卡在 D 状态（不可中断 IO）。
#   它对告警阈值的贡献只是一个参考数字，**不值得**为它压垮磁盘，
#   所以单独给它一个更短的上界，拿不到就记 n/a（下面的阈值判断不会因此误报）。
# ★ `set -o pipefail` 下，被超时杀掉的 du 会让整条管道返回 124，
#   而 `set -e` 把 `x=$(...)` 的非零退出当成致命错误直接终止脚本 ——
#   那就变成「du 超时 ⇒ 整个巡检不产出任何一行采样」，比 du 拿不到值更糟。
#   故显式吞掉。
docker_storage_gb=$(_llmgw_run_bounded "${LLMGW_DU_TIMEOUT:-30}" \
  du -s /var/lib/containers/storage/ 2>/dev/null | awk '{printf "%.1f", $1/1024/1024}' || true)
[ -n "$docker_storage_gb" ] || docker_storage_gb="n/a"

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

echo "[$(date -Iseconds)] level=$level gate=$([ "$_llmgw_single_instance_guarded" = true ] && echo guarded || echo FAIL_OPEN) disk=${used_pct}% avail=${avail_gb}GB db_llm_gateway=${db_size_gb}GB db_total=${db_all_gb}GB req_logs_hot=${req_logs_hot_size}MB mp_hot=${mp_hot_size}MB handoff=${handoff_size}MB columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB docker_storage=${docker_storage_gb}GB" >> "$LOG"
echo "[$(date -Iseconds)] top5: $top_tables" >> "$LOG"
echo "[$(date -Iseconds)] dead_tup_top3: $dead_tup_pct_top" >> "$LOG"

# === 告警去重（防 10 分钟一次的 IM 噪声）===
# 规则：
#   - 同 fingerprint + 同 level 在 cooldown 时间内不再推 IM
#   - 但 alert log 仍每条都写
#   - critical 默认 1 小时冷却；warning 30 分钟；info 不推送
if [ "$level" != "ok" ]; then
  msg="[$(date -Iseconds)] [252] ${level}"
  for a in "${alerts[@]}"; do msg+=$'\n'"$a"; done
  msg+=$'\n'""
  msg+=$'\n'"disk=${used_pct}% avail=${avail_gb}GB"
  msg+=$'\n'"llm_gateway=${db_size_gb}GB total=${db_all_gb}GB"
  msg+=$'\n'"request_logs_hot=${req_logs_hot_size}MB model_probe_runs_hot=${mp_hot_size}MB"
  msg+=$'\n'"columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB"
  msg+=$'\n'""
  msg+=$'\n'"📊 top5 tables: $top_tables"
  msg+=$'\n'"💀 dead_tup_top3: $dead_tup_pct_top"

  echo "[$(date -Iseconds)] $msg" >> "$ALERT_LOG"

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
      echo "[$(date -Iseconds)] cooldown active for $level (last=${last_ts}s ago, cooldown=${COOLDOWN_MIN}m), local log only" >> "$LOG"
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
