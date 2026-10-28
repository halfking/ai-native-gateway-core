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
# ★ 打开锁文件的那一行**不得**把 stderr 一起重定向（§143.14）：
#   exec 不带命令时，重定向施加到**当前 shell** ⇒ "exec 9>file 2>/dev/null"
#   会让本脚本此后所有 stderr 输出永久消失 ⇒ log()（写 stderr）全部失声。
#   ⇒ 用 { exec 9>file; } 2>/dev/null：组的重定向只作用于组本身，组结束即恢复；
#     fd 9 的语义不变（exec 的重定向仍然持久）。
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && { exec 9>"$_llmgw_lock_file"; } 2>/dev/null; then
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

CONTAINER=${LLMGW_PG_CONTAINER:-pg-252-pg17}
PG_USER=llm_gateway
PG_DB=llm_gateway
LOG=${LLMGW_DISK_WATCH_LOG:-/var/log/pg17-disk-watch.log}
ALERT_LOG=${LLMGW_DISK_WATCH_ALERT_LOG:-/var/log/pg17-disk-watch.alert.log}
NOTIFY=${PG17_NOTIFY_BIN:-/opt/scripts/notify.sh}
# ★ 2026-10-19（§143.8）：两个**可注入**执行点，让行为门能用假 docker / 假 df
#   驱动真实判定分支，而不是只能对源码做子串断言。
#   为什么需要 df 也可注入：本脚本的告警电平直接由 `df -P /` 的 used% 决定，
#   而开发机（macOS）的真实读数会随磁盘占用变化 ⇒ 门禁结果不可复现。
#   与 emergency-cleanup / vacuum-bloat / proactive 脚本同款注入点。
DOCKER_BIN=${DOCKER_BIN:-docker}
DF_BIN=${DF_BIN:-df}

# === 阈值（可被 conf 覆盖）===========================================
# 默认阈值基于 200 GB 磁盘 + 之前一次 80%+ 紧急扩容事件的复盘：
#   - 当时 70% 触发告警后没人看，80% 才发现 — 因此 warning 阈值定 60% 是为了
#     留出"还有空间启动清理"的时间窗。
DISK_WARN_PCT=${DISK_WARN_PCT:-60}
# IM 心跳上限（分钟）：电平与告警种类都没变时，至多隔这么久必推一次。
# ⇒ 这是「静默」的时间上界；**调小会更吵，调大更容易让人错过一个持续故障**。
PG17_HEARTBEAT_MIN=${PG17_HEARTBEAT_MIN:-1440}
DISK_CRIT_PCT=${DISK_CRIT_PCT:-75}
DISK_AVAIL_GB_WARN=${DISK_AVAIL_GB_WARN:-30}
DB_WARN_GB=${DB_WARN_GB:-20}
DB_CRIT_GB=${DB_CRIT_GB:-28}
# ★ 2026-10-22（§145）：监视对象从 _request_logs_hot（随迁移 890 退役，探针
# 变 42P01 噪声源 ~144 条/天）换成它的语义继任者 session_turns_hot（现行主写入
# hot 面，含 JSONB preview/search_text，TOAST 风险同族）。阈值沿用原值。
# 旧 env 名保留一次回退，运维脚本里已设旧名的不至于静默回到默认值。
HOT_SESSION_TURNS_MB=${HOT_SESSION_TURNS_MB:-${HOT_REQUEST_LOGS_MB:-2048}}     # session_turns_hot 2 GB 警告（JSONB TOAST 单表风险同族）
HOT_SESSION_TURNS_CRIT_MB=${HOT_SESSION_TURNS_CRIT_MB:-${HOT_REQUEST_LOGS_CRIT_MB:-3072}}
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
  # 纯 bash 回退。★ 必须**按进程组杀**，不能只 kill 直接子进程：
  #   实测（本次审计）：`kill -9 <子进程>` 之后，`bash -c 'sleep 60; …'`
  #   里那条 sleep **会活下来**。连续 3 次超时调用 = 留下 3 个孤儿。
  #   而「超时的进程留下来继续跑」正是 2026-10-10 事故的形态 ——
  #   超时这道闸若自己漏进程，就等于把事故原样复现一遍。
  #   方案取舍（实测）：函数内 `set -m` 让后台作业自成进程组，
  #   `kill -9 -$pid` 整组回收 ⇒ 0 孤儿；
  #   而 `( set -m; exec … ) &` 子 shell 写法实测**仍漏 3 个**，故不用。
  set -m
  "$@" &
  local pid=$! waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$secs" ]; then
      kill -9 -"$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      set +m
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"; local _rc=$?
  set +m
  return $_rc
}

# PGOPTIONS 走服务端超时，exec 外的 timeout 走客户端超时。两者不可互相替代：
# 只设客户端的话，连接会按时断开但**查询仍占着 PG 的进程槽**直到服务端自己放弃。
#
# ★ 每次调用都往状态文件记一行 ok/fail。**必须用文件而不是变量**：
#   调用方一律写成 `x=$(de ...)`，那是子 shell，变量自增出不来
#   （第一版就是这么写的，于是失败计数恒为 0，等于没记）。
_DE_STAT_FILE=$(mktemp "${TMPDIR:-/tmp}/llmgw-dw-stat.XXXXXX")
# ★★ 2026-10-28：这里必须用 **绝对路径** `/bin/rm`，不能写裸 `rm`。
#   起因：macOS 本机 PATH 上的 `rm` 是 mavis-trash 的**回收站 shim**
#   ⇒ 它只把字节搬进 `~/.Trash`，**磁盘不释放**。
#   实测（磁盘侧会话第 43 轮回执）：回收站里 `llmgw-dw-stat.*` 的条目
#   **每个小时约 120 条**（本脚本每次调用留一个），⇒ ⇒ **裸 `rm` 在本机 = 零回收**。
#   ⇒ `/bin/rm` 在 macOS 与 Linux 都存在（Linux 上 /bin → /usr/bin 合并）⇒ ⇒ 两端通用。
#   ⇒ ★ 同仓还有约 95 处 `trap 'rm …'`（多为 `rm -rf`）⇒ ⇒ **本轮只改这一条**
#   （★ 它是**高频**且**已被实测证实**漏空间的那一条）⇒ ⇒ 其余属同类低频项，
#   ⇒ ⇒ **交属主 / 磁盘侧裁决，不擅自批量改**（★ 判据比事实源宽 ⇒ 批量落地就是在改无辜的代码）。
trap '/bin/rm -f "$_DE_STAT_FILE"' EXIT

de() {
  local out rc
  if out=$(_llmgw_run_bounded "$LLMGW_QUERY_TIMEOUT" \
      "$DOCKER_BIN" exec \
        -e "PGOPTIONS=-c statement_timeout=${LLMGW_QUERY_TIMEOUT_MS} -c lock_timeout=5000" \
        -e "PGCONNECT_TIMEOUT=$LLMGW_CONNECT_TIMEOUT" \
        "$CONTAINER" "$@" 2>/dev/null); then
    rc=0
  else
    rc=$?
  fi
  # 空输出同样算失败：psql 成功但返回空（例如聚合无行）与「压根没跑成」
  # 在下游完全同形，都不能当成一个可信的数字往下传。
  if [ "$rc" -ne 0 ] || [ -z "$out" ]; then
    printf 'fail\n' >> "$_DE_STAT_FILE" 2>/dev/null || true
    printf '0'
    return 0
  fi
  printf 'ok\n' >> "$_DE_STAT_FILE" 2>/dev/null || true
  printf '%s' "$out"
}

used_pct=$("$DF_BIN" -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
used_gb=$("$DF_BIN" -P / | awk 'NR==2 {printf "%.1f", $3/1024/1024}')
avail_gb=$("$DF_BIN" -P / | awk 'NR==2 {printf "%.1f", $4/1024/1024}')
# ★ 2026-10-11 审计修正：这里原本是 `du -s /var/lib/containers/storage/`。
#   实测（252，负载已回落之后）该命令**跑 280 秒仍未返回** ——
#   事故当天现场有 11 个 du 卡在 D 状态，它本就是两个负载源之一。
#   我上一轮给它加了 30s 上界，结果是**把这个原本能取到值的指标永久打瞎**
#   （采样行里长期是 `docker_storage=n/a`）—— 一个"有界的失败"，
#   比一个"慢但正确"更糟，因为它让人以为这块已经有人管了。
#   ⇒ 改成 `df -P`：瞬时返回，且给出的信号（容器存储所在文件系统已用/可用）
#     才是真正可执行的告警依据。注意语义变了：这是**文件系统用量**，
#     不是容器总量，故日志字段改名 storage_fs_gb 以免误读。
#   本指标只写日志、不参与任何阈值判断（见下方 alerts 段），故不丢告警能力。
STORAGE_DIR=/var/lib/containers/storage
storage_fs_gb=$(_llmgw_run_bounded 10 "$DF_BIN" -P "$STORAGE_DIR" 2>/dev/null | awk 'NR==2 {printf "%.1f", $3/1024/1024}' || true)
[ -n "$storage_fs_gb" ] || storage_fs_gb="n/a"

db_size_bytes=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_database_size('$PG_DB')")
db_size_gb=$(awk -v b="$db_size_bytes" 'BEGIN {printf "%.2f", b/1024/1024/1024}')
db_all_gb=$(de psql -U "$PG_USER" -d postgres -tAc "SELECT round(sum(pg_database_size(datname))/1024.0/1024.0/1024.0, 2) FROM pg_database WHERE datistemplate=false" || echo 0)

turns_hot_size=$(de psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('public.session_turns_hot')/1024/1024")
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
[ "$(awk_gte "$turns_hot_size" "$HOT_SESSION_TURNS_CRIT_MB")" = "1" ] && \
  alerts+=("🔴session_turns_hot critical: ${turns_hot_size}MB, 阈值 ${HOT_SESSION_TURNS_CRIT_MB}MB")

# warning
[ "$used_pct" -ge "$DISK_WARN_PCT" ] && [ "$used_pct" -lt "$DISK_CRIT_PCT" ] && \
  alerts+=("🟡磁盘 warning: ${used_pct}% (avail=${avail_gb}GB), 阈值 ${DISK_WARN_PCT}%")
[ "$(awk_gte "$db_size_gb" "$DB_WARN_GB")" = "1" ] && [ "$(awk_gte "$DB_CRIT_GB" "$db_size_gb")" = "1" ] && \
  alerts+=("🟡llm_gateway DB warning: ${db_size_gb}GB, 阈值 ${DB_WARN_GB}GB")
awk_floor() { awk -v a="$1" 'BEGIN {i=int(a+0); print (i+0<=a+0)?i:(i+1)}'; }
avail_floor=$(awk_floor "$avail_gb")
[ "$avail_floor" -le "$DISK_AVAIL_GB_WARN" ] && \
  alerts+=("🟡磁盘可用空间 warning: ${avail_gb}GB ≤ ${DISK_AVAIL_GB_WARN}GB")
[ "$(awk_gte "$turns_hot_size" "$HOT_SESSION_TURNS_MB")" = "1" ] && [ "$(awk_gt "$HOT_SESSION_TURNS_CRIT_MB" "$turns_hot_size")" = "1" ] && \
  alerts+=("🟡session_turns_hot warning: ${turns_hot_size}MB, 阈值 ${HOT_SESSION_TURNS_MB}MB")
[ "$(awk_gte "$mp_hot_size" "$HOT_MODEL_PROBE_MB")" = "1" ] && \
  alerts+=("🟡model_probe_runs_hot warning: ${mp_hot_size}MB, 阈值 ${HOT_MODEL_PROBE_MB}MB (TTL 14天,如异常涨需排查)")
[ "$(awk_gte "$columnar_chunk_gb" "$COLUMNAR_CHUNK_GB")" = "1" ] && \
  alerts+=("🟡columnar 元数据膨胀: ${columnar_chunk_gb}GB (chunk 表), 阈值 ${COLUMNAR_CHUNK_GB}GB")
[ "$(awk_gte "$pg_wal_gb" "$PG_WAL_GB")" = "1" ] && \
  alerts+=("🟡WAL 拥堵: ${pg_wal_gb}GB, 阈值 ${PG_WAL_GB}GB")

# ★ 2026-10-11 审计修：原来这里是裸 `for a in "${alerts[@]}"`，
#   而 bash 3.2（macOS 自带）在 `set -u` 下展开**空数组**会报
#   `alerts[@]: unbound variable` 并退出 —— 也就是说「一条阈值都没触发」
#   （最常见的那种情况）反而让脚本当场死掉、连采样行都写不出来。
#   本机无 docker/bash 3.2 上跑本脚本时必现，已实测复现。
#   bash 4.4+ 才把空数组展开当合法，故 252 上一直没暴露。
#   修法与本仓 deploy 侧「空 ARGS 在 bash 3.2 下 unbound」同源：按个数守卫。
level=ok
_de_alert_count=${#alerts[@]}
if [ "$_de_alert_count" -gt 0 ]; then
  level=warning
  for a in "${alerts[@]}"; do
    case "$a" in 🔴*) level=critical; break;; esac
  done
fi

# === 量具可用性判定（2026-10-11 审计新增）===============================
# ★ 这是本次事故最该被记住的一课：
#   `de()` 失败返回 "0"，而上面每一条阈值判断都是「>= 阈值」——
#   于是**取不到值**与**一切正常**在判据里完全同形。
#   2026-10-10 全天 psql 一条都取不到，采样行却始终是 `level=ok`，
#   dead_tup 还会显示成 `clean`：**监控在完全瞎掉的状态下报了 8 小时健康**。
#   那 8 小时正是 154 被 Redis/PG 拖垮、而无人从监控侧发现的时间窗。
#
#   ⇒ 「查不到」必须与「没问题」分开判。本目录契约里 3 = 量具不可用，
#     这里落到 gauge_unavailable，且**永不允许**降级成 ok。
_de_total=$(wc -l < "$_DE_STAT_FILE" 2>/dev/null | tr -d ' ') || _de_total=0
_de_fail=$(grep -c '^fail$' "$_DE_STAT_FILE" 2>/dev/null) || _de_fail=0
: "${_de_total:=0}"; : "${_de_fail:=0}"

if [ "$_de_fail" -gt 0 ]; then
  alerts+=("⚠️量具不可用: ${_de_fail}/${_de_total} 个 DB 指标取不到（psql/exec 超时或失败）。
   ★ 这**不等于** PG 健康。本次运行对 DB 相关维度**没有结论**。")
  # critical 磁盘告警比「量具不可用」更紧急，保留；否则明确标为量具不可用。
  [ "$level" = "ok" ] && level=gauge_unavailable
  # 这两个字段在「查不到」与「正常为空」之间同形，查不到时不准说 clean/n/a。
  case "$top_tables" in "0"|"n/a") top_tables="unavailable(量具故障)";; esac
  case "$dead_tup_pct_top" in "0"|"clean") dead_tup_pct_top="unavailable(量具故障)";; esac
fi

echo "[$(date -Iseconds)] level=$level gate=$([ "$_llmgw_single_instance_guarded" = true ] && echo guarded || echo FAIL_OPEN) gauge=${_de_fail}/${_de_total} disk=${used_pct}% avail=${avail_gb}GB db_llm_gateway=${db_size_gb}GB db_total=${db_all_gb}GB turns_hot=${turns_hot_size}MB mp_hot=${mp_hot_size}MB handoff=${handoff_size}MB columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB storage_fs=${storage_fs_gb}GB" >> "$LOG"
echo "[$(date -Iseconds)] top5: $top_tables" >> "$LOG"
echo "[$(date -Iseconds)] dead_tup_top3: $dead_tup_pct_top" >> "$LOG"

# === 告警去重（IM 侧；**本地 alert log 一条不减**）====================
# ★ 2026-10-19（§143.8）改了判定方式，规则如下：
#   - **本地 alert log 仍然每轮都写**（取证用）。
#     去噪只作用于 IM 推送 —— 删本地日志就是「让错误更难被翻到」，
#     与本仓 §143.8 的教训正好相反（按错误口径定级会导向加轮转）。
#   - IM 推送改为「**电平跳变 / 告警种类集变化 / 心跳**」三者之一触发：
#       ① 电平跳变（ok↔warning↔critical↔gauge_unavailable）—— 永远推，且不受冷却约束
#       ② 告警种类集变化（同电平但冒出新问题）—— 受冷却约束
#       ③ 心跳：状态持续未复述，至多 PG17_HEARTBEAT_MIN（默认 1440 分钟）后必推一次
#   - ⚠ ③ 是整套改动的**安全下限**：只做①②的话，一个持续存在的故障
#     会在第一次推送后永远沉默 —— 告警就从「吵」变成「瞎」。
#   证据（252 只读，2026-10-19）：原实现 3 个月推了 12,371 次，
#     其中**只有 56 次**是真的电平跳变，其余 12,315 次（99.5%）在重复推同一状态。
if [ "$level" != "ok" ]; then
  msg="[$(date -Iseconds)] [252] ${level}"
  for a in "${alerts[@]}"; do msg+=$'\n'"$a"; done
  msg+=$'\n'""
  msg+=$'\n'"disk=${used_pct}% avail=${avail_gb}GB"
  msg+=$'\n'"llm_gateway=${db_size_gb}GB total=${db_all_gb}GB"
  msg+=$'\n'"session_turns_hot=${turns_hot_size}MB model_probe_runs_hot=${mp_hot_size}MB"
  msg+=$'\n'"columnar_chunk=${columnar_chunk_gb}GB wal=${pg_wal_gb}GB"
  msg+=$'\n'""
  msg+=$'\n'"📊 top5 tables: $top_tables"
  msg+=$'\n'"💀 dead_tup_top3: $dead_tup_pct_top"

  echo "[$(date -Iseconds)] $msg" >> "$ALERT_LOG"

  if [ -x "$NOTIFY" ]; then
    # 默认有 cooldown:critical 1h / warning 30min
    #
    # ★ `*)` 兜底是必需的，不是防御性冗余：
    #   本 case 的结果直接进 `$((COOLDOWN_MIN * 60))`，一旦某个新 level
    #   没有对应分支，COOLDOWN_MIN 就是未绑定变量，而这里是 `set -u`
    #   ⇒ 脚本会在告警路径上崩掉、IM 推不出去。2026-10-11 审计在下面
    #   的 fingerprint 那行就抓到过一个同类的真事故（见 ts 说明）。
    case "$level" in
      critical)          COOLDOWN_MIN=${PG17_CRIT_COOLDOWN_MIN:-60};;
      warning)           COOLDOWN_MIN=${PG17_WARN_COOLDOWN_MIN:-30};;
      info)              COOLDOWN_MIN=${PG17_INFO_COOLDOWN_MIN:-1440};;
      gauge_unavailable) COOLDOWN_MIN=${PG17_GAUGE_COOLDOWN_MIN:-30};;
      *)                 COOLDOWN_MIN=30;;
    esac
    # notify.sh 只认 critical/warning/info 这类严重度，而
    # 「监控自己瞎了」按本目录教训应当**按 critical 报**——10-10 那 8 小时
    # 正是没人从监控侧看到。所以这里做映射，标题仍保留量具不可用字样。
    notify_level=$level
    case "$notify_level" in critical|warning|info) ;; *) notify_level=critical;; esac

    # ★ 2026-10-11 审计修掉的真缺陷：这里原本在算指纹时写了 "${ts:0:7}"，
    #   而 `ts` 在整个脚本里**从未被赋值**（模板残留）。
    #   macOS bash 3.2 静默取空（fingerprint 退化，尚可用），
    #   但生产的 bash 4.4 上 `set -u` 直接报 `ts: unbound variable` 并退出 1
    #   —— 已在 252 实测复现。后果：**任何一次真告警都会在最后一步崩掉，
    #   IM 永远推不出去**（alert log 写了，但没人收到）。
    #   即 252 的告警投递通路在这次事故之前就是断的。
    # ── 指纹去抖（2026-10-19 §143.8）────────────────────────────────
    # ★ 这里原来把**告警正文**整个喂进 md5：
    #     fingerprint=$(printf '%s\n%s\n%s' "$level" "$(… "${alerts[@]}" | md5sum …)" "$(date +%Y-%m)" | md5sum …)
    #   而正文里全是会抖的**取值**（`磁盘 critical: 72% (avail=54GB), 阈值 90%`）。
    #   ⇒ 磁盘百分比 71↔72、avail 53↔54 都产生**新指纹**
    #   ⇒ 冷却判定永远命中不到上一条 ⇒ **冷却形同虚设**。
    #   252 实测（2026-10-19，取证在 §143.8.1）：
    #     cooldown 文件 906 行（指纹种类数），其中 767 行已超 24h 未复现（85% 是死重量）
    #     alert log 12,371 条推送记录（3 个月）
    #     最近 12 个指纹的时间戳间隔 = 584~615 秒 ≈ 脚本的 10 分钟周期
    #   ⇒ **每一轮都推 IM，一次都没被冷却拦住过**。
    # ⇒ 修法：指纹只取「告警的种类」，把数值归一化后再哈希。
    #   「种类」= 去掉全部数字后的文本 + level。同一个磁盘问题在 71% 和 72%
    #   是同一个种类（该推一次），而磁盘问题与 WAL 拥堵是两个种类（该分开推）。
    _alert_kinds=$(printf '%s\n' "${alerts[@]}" \
      | sed -E 's/[0-9]+(\.[0-9]+)?/N/g' \
      | LC_ALL=C sort -u)
    fingerprint=$(printf '%s\n%s' "$level" "$_alert_kinds" | md5sum | cut -d' ' -f1)
    state_file=${PG17_DISK_WATCH_STATE:-/var/tmp/pg17-disk-watch.alertstate}
    now_epoch=$(date +%s)
    # 状态文件只有一行：<fingerprint>:<level>:<epoch>
    # ⚠ 读不到 / 读坏了都让 *_prev 为空 ⇒ 下面的 level 比对必然成立 ⇒ 推送。
    #   方向是**多推**而不是少推 —— 状态文件坏掉不该让告警静默（2026-10-10 的教训）。
    prev_line=$(head -1 "$state_file" 2>/dev/null || true)
    prev_fp=${prev_line%%:*}
    _rest=${prev_line#*:}
    prev_level=${_rest%%:*}
    prev_ts=${_rest##*:}
    [[ "$prev_ts" =~ ^[0-9]+$ ]] || prev_ts=0

    heartbeat_min=${PG17_HEARTBEAT_MIN:-1440}   # 持续未复述的心跳（默认每天一次）

    push_im=true
    if [ "$level" != "$prev_level" ]; then
      # 电平跳变（含 ok↔warning↔critical↔gauge_unavailable）永远推。
      # ⚠ 这一支**不**受冷却约束：warning→critical 若因冷却被吞掉，
      #   正好在最该让人看见的时刻静默 —— 与「冷却防噪声」的初衷正好相反。
      push_reason="电平跳变 ${prev_level:-none} -> $level"
    elif [ $((now_epoch - prev_ts)) -ge $((heartbeat_min * 60)) ]; then
      # 心跳：状态一直没变，但**不许就此永远沉默**。
      # ★ 这是本节最重要的护栏：把「电平判定」改成「跳变判定」时，
      #   最容易犯的错就是让一个持续存在的故障永远不再出现
      #   —— 那样告警就从「吵」变成了「瞎」，而 2026-10-10 正是后者造成的 8 小时盲区。
      # ⇒ 所以任何一次抑制都**有明确的时间上界**：至多 heartbeat_min 分钟后必推一次。
      push_reason="持续未复述 ${heartbeat_min} 分钟（心跳）"
    elif [ "$fingerprint" != "$prev_fp" ] \
         && [ $((now_epoch - prev_ts)) -ge $((COOLDOWN_MIN * 60)) ]; then
      # 同电平但「告警种类集」变了（如新冒出 WAL 拥堵）⇒ 值得说，但要受冷却约束，
      #   否则某个阈值边缘的量会来回抖动，把 IM 打成新的 10 分钟风暴。
      push_reason="告警种类集变化（距上次 ${COOLDOWN_MIN} 分钟以上）"
    else
      push_im=false
      push_reason="状态未变（距上次 $(( (now_epoch - prev_ts) / 60 )) 分钟 < 心跳 ${heartbeat_min} 分钟）"
    fi
    echo "[$(date -Iseconds)] im-gate level=${level} fp=${fingerprint:0:8} prev_level=${prev_level:-none} age=$((now_epoch - prev_ts))s -> $([ "$push_im" = true ] && echo "PUSH ($push_reason)" || echo "SUPPRESS ($push_reason)")" >> "$LOG"

    if [ "$push_im" = "true" ]; then
      "$NOTIFY" --level "$notify_level" --title "252 ${level}" --body "$msg" || true
      # 写入状态（单行覆盖，不是追加）
      mkdir -p "$(dirname "$state_file")" 2>/dev/null
      printf "%s:%s:%s\n" "$fingerprint" "$level" "$now_epoch" > "${state_file}.tmp" 2>/dev/null \
        && mv "${state_file}.tmp" "$state_file" 2>/dev/null || true
    fi
  fi
fi

# === 退出码（对齐本目录契约）=============================================
#   0  正常
#   1  检出阈值问题（warning / critical）
#   3  量具不可用 —— 本次对 DB 维度**没有结论**，**不等于** PG 健康
#
# ★ 为什么要显式写：cron 那行是 `>/dev/null 2>&1`，rc 到不了 cron 的 job 状态
#   （本目录已登记该缺口，见 etc.cron.d.pg17 注释 C7-P3-2）。但手工/门禁运行
#   时 rc 是唯一能一眼区分「查过了没问题」「查过了有问题」「压根没查成」的信号，
#   而这三种在 10-10 那 8 小时里**全是同一个 level=ok**。
case "$level" in
  gauge_unavailable) exit 3 ;;
  ok)                exit 0 ;;
  *)                 exit 1 ;;
esac
