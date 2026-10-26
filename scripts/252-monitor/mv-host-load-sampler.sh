#!/usr/bin/env bash
# 252 宿主机级负载 × matview 刷新链的**同窗**采样器。
#
# ── 为什么需要它（R98 的直接后续）────────────────────────────────────────
#
# ★★ R98 轮实测出的一件反直觉的事：
#   · 2026-10-25 02:05 那轮 `session_client_task_matrix` 的 REFRESH 耗时 **191.72 s**；
#   · 而**同一时刻**的 PG 内部量具**全部显示「空闲」**：
#       `wait_event_type` 全程为空、`pg_blocking_pids` 为空、
#       `pg_stat_activity` 的 `active` 只有 0~1、`pg_stat_database.temp_bytes` 恒定（零 spill）、
#       252 上无 >30 s 长事务；
#   · ★★ **而 252 宿主机同时刻 `iostat -x` 显示：`%idle` 只有 2.64 %~3.06 %、
#     `vda` `%util` 72.65 %~83.10 %、读队列 `aqu-sz` 高达 7.88。**
#
# ⇒ ⇒ ★★★ **★ 「`wait_event_type` 为空」只说明「没在等 PG 内部的资源」，★ 不说明「拿到了 CPU」。★★**
# ⇒ ⇒ ★★★ **★ 而 `pg_stat_activity` 会把这种状态读成「库很闲」——★ 这正是 R98 第一版结论出错的地方。★★**
#
# ★★ ⇒ **测这种负载必须用宿主机级量具**（`vmstat` / `iostat` / `/proc/loadavg`），
# ★★ **而 R98 那一轮的宿主机读数取自 02:18、刷新发生在 02:05 ⇒ ★★ 两个时刻不严格同窗。★★**
#
# ★★ ⇒ **本脚本解决的就是「同窗」**：★ **每 15 秒**采一次，
# ★★ **同一次采样里既记宿主机负载，也记「此刻正在跑的 REFRESH 语句的 pid + query_start」**
# ★★ ⇒ ⇒ **⇒ 每次刷新都落在一批采样点上 ⇒ ⇒ 「刷新耗时」与「它跑的那几秒的宿主机负载」可以逐条配对。★★**
#
# ── 契约 ──────────────────────────────────────────────────────────────────
#
# ★ **只读**：★ 本脚本只跑 `SELECT` 与读取 `/proc`；**不写库、不改配置、不重启任何东西**。
# ★ **低干扰**：★ 每 15 秒 1 次 `vmstat 1 2`（≈ 1 s）+ 1 次 `psql` 单查询。
# ★ **可自证**：★ 每行带 `epoch_ms` 与 `seq`；**实际间隔 > 3 × INTERVAL ⇒ 日志写 `WARN: 采样间隔异常`**；
#   日志另含**启动时刻 / 结束时刻 / 总采样数**（死循环靠外部 kill ⇒ trap 落汇总）。
# ★ **只追加**：★ 每行 append，不重写历史；跨天换文件。
#
# ── 用法 ──────────────────────────────────────────────────────────────────
#
#   scripts/252-monitor/mv-host-load-sampler.sh [OUT_DIR] [INTERVAL_SECS]
#
#   默认 OUT_DIR=/var/log/llm-mv-host-load  INTERVAL_SECS=15
#
# ★ 252 部署形态（★ PG 跑在容器 pg-252-pg17 里，★ 宿主机 127.0.0.1:5432 连不上）：
#     PSQL_CMD="docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway" \
#       scripts/252-monitor/mv-host-load-sampler.sh /var/log/llm-mv-host-load 15
# ★ 口令**绝不写进脚本**；容器内 socket 认证无需口令。
#
# 产出：
#   $OUT_DIR/mv-host-load-YYYYMMDD.tsv        每采样一行（表头见下）
#   $OUT_DIR/mv-host-load-sampler.log          运行日志（含启动/结束时刻与采样计数）
#
# ── TSV 列定义（★ 刻意做成纯数值，便于直接进 awk/join）────────────────────
#
#   seq  epoch_ms  iso  load1  procs_running  cpu_user  cpu_sys  cpu_iowait  cpu_steal  cpu_idle
#   pg_active  pg_total  pg_wait_lock  pg_wait_io
#   io_blks_read  io_blks_hit  io_temp_bytes  io_tup_ins  io_tup_upd  io_tup_del
#   refresh_pid  refresh_age_ms  refresh_query
#
# ★ `refresh_pid` 为空 ⇒ 该采样时刻没有 REFRESH 在跑。
# ★ `refresh_age_ms` = 该 REFRESH 语句已运行的毫秒数（由 `pg_stat_activity.query_start` 算）。
# ★ ⇒ **后处理**用「pid 出现 ⇒ 出现时刻；pid 消失 ⇒ 结束时刻」推出每次刷新的耗时，
# ★   **而它跑过的那几秒的宿主机负载就在相邻的采样行里** ⇒ ⇒ ★★ **同窗配对成立。★★
#
# ★★ **2026-10-25 04:0x 实测修掉一个量具缺陷（★ 这正是「量具自己会坏」的活证据）**：
# ★ 第二次抓到 REFRESH 时 `refresh_age_ms = -54`（★ **负数**）⇒ ★ **不是 REFRESH 跑了负时间**，
# ★ ★ 而是**年龄公式用错了时钟**：`now()` 是**事务开始时间**（本脚本每次采样都是新事务 ⇒ `now()` = 连接时刻），
# ★ ★ 而 `query_start` 是**该语句真正开始的时刻** ⇒ ★★ **当 REFRESH 恰好在「连接之后、语句执行之前」启动时，
# ★ ★ `now() - query_start < 0`** ⇒ ★★ **负年龄会污染「刷新耗时」的后处理推导**。
# ★ ★ ⇒ **修法：年龄一律用 `clock_timestamp()`（真实墙上钟）**，★ 不再用 `now()`（事务钟）。
# ★ ★ ⇒ **教训同族**：★ **凡「从库里取时间差」的读数，★ 先问「这个钟是事务钟还是墙上钟」** ⇒ ★ **事务钟在长事务里会漂**。

set -uo pipefail

OUT_DIR="${1:-/var/log/llm-mv-host-load}"
INTERVAL="${2:-15}"

# ── PG 连接方式（★ 单一可覆盖入口：PSQL_CMD）────────────────────────────────
# ★ 252 上的硬事实（已实测）：
#   · `127.0.0.1:5432` **连不上**（PG 容器 pg-252-pg17 映射到 172.16.2.210:5432）；
#   · 宿主机 psql 走 172.16.2.210:5432 **需要口令** ⇒ 口令绝不写进本脚本；
#   · `docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway` 走容器内 socket 认证，**无需口令**。
# ★ ⇒ 252 的正确部署形态：
#     PSQL_CMD="docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway"
# ★ 默认形态（宿主机 psql）保留给其它环境：走标准 libpq 环境变量。
PSQL_CMD="${PSQL_CMD:-psql}"
PGHOST="${PGHOST:-172.16.2.210}"     # ★ 旧默认 127.0.0.1 在 252 上必然 Connection refused
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-llm_gateway}"
PGDATABASE="${PGDATABASE:-llm_gateway}"

# ★ 只读 + 不编译（★ JIT 对采样毫无意义，且会污染计时）
export PGOPTIONS="-c statement_timeout=30s -c jit=off -c default_transaction_read_only=on"

# ★ 把 PSQL_CMD 按空格切成数组再展开。
#   ⚠ **不能**写成 `"$PSQL_CMD" ...`：那样 `docker exec ...` 会被当成**单个可执行文件名** ⇒ 必然失败。
IFS=' ' read -r -a PSQL_ARGS <<< "$PSQL_CMD"

# ★ docker/podman exec **不继承宿主机环境变量**（实测：宿主机 export 的 PGOPTIONS 进不去容器，
#   default_transaction_read_only 由 on 退回 off）⇒ 容器路径必须显式 `-e` 注入。
PSQL_EXEC_ARGS=()
_PSQL_IS_CTL=0
if [[ "${PSQL_ARGS[0]:-}" == "docker" || "${PSQL_ARGS[0]:-}" == "podman" ]]; then
  _PSQL_IS_CTL=1
fi
_PSQL_INJECTED=0
for ((_i = 0; _i < ${#PSQL_ARGS[@]}; _i++)); do
  PSQL_EXEC_ARGS+=("${PSQL_ARGS[$_i]}")
  if [[ "$_PSQL_IS_CTL" == "1" && "$_PSQL_INJECTED" == "0" && "${PSQL_ARGS[$_i]}" == "exec" ]]; then
    PSQL_EXEC_ARGS+=(-e "PGOPTIONS=$PGOPTIONS")
    _PSQL_INJECTED=1
  fi
done

mkdir -p "$OUT_DIR" || { echo "FATAL: 无法创建 $OUT_DIR" >&2; exit 64; }
LOG="$OUT_DIR/mv-host-load-sampler.log"

TSV="$(date +%Y%m%d).tsv"
TSV_PATH="$OUT_DIR/mv-host-load-$TSV"

if [[ ! -s "$TSV_PATH" ]]; then
  printf 'seq\tepoch_ms\tiso\tload1\tprocs_running\tcpu_user\tcpu_sys\tcpu_iowait\tcpu_steal\tcpu_idle\tpg_active\tpg_total\tpg_wait_lock\tpg_wait_io\tio_blks_read\tio_blks_hit\tio_temp_bytes\tio_tup_ins\tio_tup_upd\tio_tup_del\trefresh_pid\trefresh_age_ms\trefresh_query\n' > "$TSV_PATH"
fi

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*" >> "$LOG"; }

# ★ epoch 毫秒（★ 采样间隔检测必须用毫秒差，★ 不能用 date 的秒精度）
now_ms() {
  local _s _n
  _s="$(date +%s)"
  _n="$(date +%N 2>/dev/null | cut -c1-3)"
  [[ "$_n" =~ ^[0-9]+$ ]] || _n=0
  printf '%s\n' "$(( _s * 1000 + 10#$_n ))"
}

# ★ 采样间隔阈值（★ 3 × INTERVAL 秒）；用整数秒，★ 不依赖 INTERVAL 一定是整数
INTERVAL_INT="${INTERVAL%%.*}"
[[ "$INTERVAL_INT" =~ ^[0-9]+$ ]] || INTERVAL_INT=15

SEQ=0
SAMPLES=0
LAST_MS=""
SLEEP_PID=""
START_MS="$(now_ms)"
START_ISO="$(date '+%FT%T%z')"
log "sampler start: out=$TSV_PATH interval=${INTERVAL}s psql_cmd=$PSQL_CMD pg=${PGHOST}:${PGPORT}/${PGDATABASE} start_iso=$START_ISO start_epoch_ms=$START_MS"

# ★ 本脚本是 `while :` 死循环 ⇒ 靠外部 kill；trap 负责落「结束时刻 / 总采样数」汇总。
trap 'kill "${SLEEP_PID:-}" 2>/dev/null; log "sampler stop: seq=$SEQ samples=$SAMPLES elapsed_s=$(( ($(now_ms) - START_MS) / 1000 )) start_iso=$START_ISO end_iso=$(date "+%FT%T%z")"; exit 0' TERM INT

while :; do
  # ── 宿主机侧（★ 用 vmstat 而不是 iostat：★ vmstat 每行 1 s、开销远小于 iostat）──
  # vmstat 1 2 ⇒ 第 1 行是开机以来的均值（★ 不取），第 2 行是最近 1 s 的采样。
  # ★ 只跑 **一次** vmstat（★ 旧版跑了两遍：★ 第一遍的取值被完整丢弃 ⇒ 每采样白付 ~1 s 开销）
  # ★ vmstat 的字段序：procs(running,blocked) memory(swapped,free,buffers,cache) swap(io,other)
  #   disk(io,in,other) system(in,page,other) cpu(user,nice,sys,idle,iowait,irq,softirq,steal)
  # ⇒ 第 3..7 个字段是 (r, b, swpd, free, buff, cache) ⇒ 需按位取。★ 用 awk 取更稳。
  VM2="$(vmstat 1 2 2>/dev/null | tail -1 | awk '{print $1"\t"$2"\t"$13"\t"$14"\t"$15"\t"$16"\t"$17}')"
  # ★ awk 字段：1=running 2=blocked | 13=user 14=system 15=iowait 16=steal 17=idle
  CPU_USER=$(cut -f3 <<< "$VM2"); CPU_SYS=$(cut -f4 <<< "$VM2")
  CPU_WA=$(cut -f5 <<< "$VM2"); CPU_STEAL=$(cut -f6 <<< "$VM2"); CPU_IDLE=$(cut -f7 <<< "$VM2")
  PROCS_RUN=$(cut -f1 <<< "$VM2")
  LOAD1=$(cut -d' ' -f1 /proc/loadavg)

  # ── PG 侧（★ 一条查询取全部：★ PG 侧与宿主机侧必须同刻 ⇒ ★ 放在同一次采样里）──
  ROW="$("${PSQL_EXEC_ARGS[@]}" -X -tAX -F$'\t' -c "
SELECT
  (SELECT count(*) FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid() AND state='active'),
  (SELECT count(*) FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid()),
  (SELECT count(*) FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid() AND wait_event_type='Lock'),
  (SELECT count(*) FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid() AND wait_event_type='IO'),
  (SELECT blks_read FROM pg_stat_database WHERE datname=current_database()),
  (SELECT blks_hit FROM pg_stat_database WHERE datname=current_database()),
  (SELECT temp_bytes FROM pg_stat_database WHERE datname=current_database()),
  (SELECT tup_inserted FROM pg_stat_database WHERE datname=current_database()),
  (SELECT tup_updated FROM pg_stat_database WHERE datname=current_database()),
  (SELECT tup_deleted FROM pg_stat_database WHERE datname=current_database()),
  coalesce((SELECT pid::text FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid()
      AND query ILIKE 'REFRESH MATERIALIZED VIEW%' ORDER BY pid LIMIT 1), ''),
  coalesce((SELECT (extract(epoch from (clock_timestamp()-query_start))*1000)::bigint::text
    FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid()
      AND query ILIKE 'REFRESH MATERIALIZED VIEW%' ORDER BY pid LIMIT 1), ''),
  coalesce((SELECT left(replace(query, chr(10), ' '), 60)
    FROM pg_stat_activity
    WHERE backend_type='client backend' AND pid<>pg_backend_pid()
      AND query ILIKE 'REFRESH MATERIALIZED VIEW%' ORDER BY pid LIMIT 1), '')" 2>/dev/null)"
  PSQL_RC=$?

  if [[ -z "$ROW" ]]; then
    # ★★ 采样失败**必须显式留痕**（★ R97 的教训：★ 监控静默失明比报错更糟）
    log "WARN: psql 采样失败 rc=${PSQL_RC}（本行留空）"
    # ★ 13 个空字段（12 个 \t）⇒ 与表头 23 列对齐（★ 前 10 列 + 本行 13 列 = 23）
    ROW=$'\t\t\t\t\t\t\t\t\t\t\t\t'
  fi

  SEQ=$((SEQ+1))
  SAMPLES=$((SAMPLES+1))
  NOW_MS="$(now_ms)"

  # ★★ 自证：采样间隔漂移检测（★ 用 epoch_ms 差算，★ 不能用 date 的秒精度）。
  #   实际间隔 > 3 × INTERVAL ⇒ 采样卡住 / 漏采 ⇒ 显式留痕。
  if [[ -n "$LAST_MS" ]]; then
    GAP_MS=$(( NOW_MS - LAST_MS ))
    if (( GAP_MS > 3 * INTERVAL_INT * 1000 )); then
      log "WARN: 采样间隔异常 seq=$SEQ gap_s=$(( GAP_MS / 1000 )) threshold_s=$(( 3 * INTERVAL_INT ))"
    fi
  fi
  LAST_MS="$NOW_MS"

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$SEQ" "$NOW_MS" "$(date '+%FT%T%z')" "$LOAD1" "$PROCS_RUN" \
    "$CPU_USER" "$CPU_SYS" "$CPU_WA" "$CPU_STEAL" "$CPU_IDLE" \
    "$ROW" >> "$TSV_PATH"

  # ★ 跨天换文件（★ 避免一个文件无限长 ⇒ 便于按天归档与对齐）
  #   ⚠ 旧写法 `[[ "$(date +%Y%m%d)" != "$TSV" ]]` 与 `$TSV`（★ **带 .tsv 后缀**）比较
  #     ⇒ **恒为真** ⇒ 每轮都「换文件」⇒ 每轮都把刚写的行截断掉（★ 只剩表头）。
  if [[ "$(date +%Y%m%d).tsv" != "$TSV" ]]; then
    TSV="$(date +%Y%m%d).tsv"
    TSV_PATH="$OUT_DIR/mv-host-load-$TSV"
    printf 'seq\tepoch_ms\tiso\tload1\tprocs_running\tcpu_user\tcpu_sys\tcpu_iowait\tcpu_steal\tcpu_idle\tpg_active\tpg_total\tpg_wait_lock\tpg_wait_io\tio_blks_read\tio_blks_hit\tio_temp_bytes\tio_tup_ins\tio_tup_upd\tio_tup_del\trefresh_pid\trefresh_age_ms\trefresh_query\n' > "$TSV_PATH"
    log "rolled over to $TSV_PATH"
  fi

  # ★ 后台 sleep + wait ⇒ SIGTERM 能立刻打断等待（★ 否则要等满 INTERVAL 才响应，trap 汇总会迟到）
  sleep "$INTERVAL" &
  SLEEP_PID=$!
  wait "$SLEEP_PID" 2>/dev/null
  SLEEP_PID=""
done
