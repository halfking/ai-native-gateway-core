#!/usr/bin/env bash
# =============================================================================
# ursm-snapshot-slot-refresh.sh —— URSM 快照基线汇总表的【每日刷新】作业
#
# 2026-10-22（R75 轮，P11-R75）新增。
#
# -----------------------------------------------------------------------------
# 它为什么存在（★ 这是一个**真缺陷**的修复，不是「顺手加个优化」）
# -----------------------------------------------------------------------------
# 迁移 892 建了汇总表 public.ursm_snapshot_slot_daily 与幂等填充函数
# public.refresh_ursm_snapshot_slot_daily(days_back, granularity)，
# 巡检脚本 ursm-snapshot-health.sh 也据此多了一条「汇总表路」（快路）。
#
# ★★★ 但是：**全仓没有任何地方会再调用那个函数**。
#   892 迁移里只在【首次部署时】回填一次（8 天 × 两种粒度），
#   之后那张表就【再也不动了】——而它的内容本该每天滚动。
#   ⇒ 后果（逐日推演）：
#     · 第 1 天起，汇总表里【最老的一天】停在部署那天；
#     · 快路的基线窗口是 [今天-7天, 今天)，于是可用的历史**一天比一天少**；
#     · 到第 8 天左右，快路在窗口内【一行都读不到】
#       ⇒ 全部桶判成 NO-BASELINE ⇒ ★★ **巡检静默失明**
#         （与 892 迁移头里「回填 0 行 ⇒ 巡检安静地失明」是同一族失败模式）。
#   ⇒ ★ 所以「落 892 就治掉元凶」这句话**当时是不成立的**：
#     少了本脚本，892 落地反而会把巡检从「慢但对」变成「快但盲」。
#
# ★ 同时，巡检脚本的【基线覆盖探针】也一并修正了（同一轮）：
#   原判据 `min(day) <= 今天-7天` 只问「表里有没有【足够老】的数据」，
#   而快路真正需要的是「有没有【足够新】的数据」⇒ 表停更时依然切快路。
#   现判据 = 「窗口内的每一天都有数据」（count(DISTINCT day) == 基线天数）
#   ⇒ 任何一天缺 ⇒ 回退源表路 ⇒ **慢，但不失明**（与既有哲学一致）。
#
# -----------------------------------------------------------------------------
# 它与巡检脚本的分工（★ 为什么要单独一个脚本，而不是塞进巡检里）
# -----------------------------------------------------------------------------
#   ursm-snapshot-health.sh   【纯只读】—— 它的性质自 §143.14 起就是只读巡检
#   ursm-snapshot-slot-refresh.sh  【写入型】—— 只写汇总表，**不碰任何源表**
#
# ★ 为什么「怎么算」放在 SQL 函数里（892 的设计，沿用）：
#   ① 巡检脚本保持纯只读 ⇒ 「巡检能不能写」永远不是一个待查项；
#   ② 函数幂等 ⇒ 任何调用方（cron / Go 后台作业 / 手工）都能安全重跑；
#   ③ 判定输入逐字不变 ⇒ 本作业**不碰判据**。
#
# -----------------------------------------------------------------------------
# 自我修复（★ 与「只补昨天」的朴素做法不同）
# -----------------------------------------------------------------------------
#   稳态：补最近 2 天（昨天的行数 + 前天的迟到行）⇒ 每次只扫 ~2 天
#         （生产实测：8 天 = 30,311,927 行 ≈ 235 s；2 天 ≈ 1/4 量级）
#   修复：若窗口内的天数【不足基线天数】（例如某次运行失败、或作业停了几天），
#         ⇒ 改用「补满基线窗 + 1 天」重算 ⇒ **空洞能被补回来**
#   ⇒ ⇒ 本作业**不会**因为一次失败而永久留洞（朴素的「只补昨天」会）。
#
# -----------------------------------------------------------------------------
# 退出码（沿用本目录的 0/1/3 分级契约，cron_registration_test.go 认这套）
#   0  刷新成功，且窗口覆盖已满足
#   1  刷新跑完了，但窗口覆盖【仍不足】（数据侧问题：源表当天没数据等）
#   3  量具不可用（函数不存在 / SQL 失败 / 权限不足）—— **必须响亮失败**
#   64 配置非法（GRANULARITY 之类）
#
# ⚠ **不要**把本脚本的输出 >/dev/null 2>&1 掉：它【会写库】，
#   一次静默失败 = 汇总表停更 = 巡检静默失明（上面那条链）。
#
# 部署：放到 252 的 /opt/scripts/，cron 里加一行（见 etc.cron.d.pg17）。
# =============================================================================
set -euo pipefail

# === 单实例闸门（与本目录其它巡检同款，inline 不 source helper）==============
# 理由见 ursm-snapshot-health.sh 头部：「漏一条 = 删一条」的整文件覆盖契约下，
# 不引入跨文件耦合。
LLMGW_LOCK_DIR=${LLMGW_LOCK_DIR:-/var/run/lock}
_llmgw_lock_file="$LLMGW_LOCK_DIR/llmgw-$(basename "$0" .sh).lock"

_llmgw_single_instance_guarded=false
# ★ 打开锁文件的那一行**不得**把 stderr 一起重定向（§143.14）：
#   "exec 9>file 2>/dev/null" 会让本脚本此后所有 stderr 输出永久消失
#   ⇒ 而本脚本的结论**只**走 stderr（log()）⇒ 那就是彻底失声。
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && { exec 9>"$_llmgw_lock_file"; } 2>/dev/null; then
  if ! flock -n 9; then
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: $(basename "$0") 上一轮仍在运行（lock=${_llmgw_lock_file}），本轮不重复执行" \
      >> "${LLMGW_LOCK_SKIP_LOG:-/var/log/llmgw-single-instance-skip.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
[ -f "$CONF" ] && source "$CONF"
: "${CONTAINER:=pg-252-pg17}"
: "${PG_USER:=llm_gateway}"
: "${PG_DB:=llm_gateway}"

# 统计粒度。★ 生产用 hour（巡检脚本的默认 GRANULARITY 也是 hour）。
#   minute 形支持，但生产没有在用 ⇒ 默认**不**补 minute（那会把成本翻倍）。
: "${GRANULARITY:=hour}"
case "$GRANULARITY" in
  hour|minute) ;;
  *) echo "[$(date '+%F %T')] GRAMULARITY 只能是 hour 或 minute，收到: $GRANULARITY" >&2; exit 64 ;;
esac

# 基线窗口天数（与巡检脚本的 BASELINE_DAYS 同义）。
: "${BASELINE_DAYS:=7}"
# ★ 2026-10-22 12h 审计轮补：BASELINE_DAYS **必须**是正整数。
#   理由与 GRANULARITY 同款——但它的后果更隐蔽：BASELINE_DAYS 同时进 SQL
#   （make_interval(days => $BASELINE_DAYS)）与数值判定（覆盖天数比较）。
#   非正数（如 0）⇒ 窗口恒空 ⇒ 每次都落 repair 路、每次 exit 1，
#   ⇒ **永不自我恢复**（作业看着在跑，永远是红的）。
#   ⇒ 与 GRANULARITY 同款处理：非法即 exit 64（配置非法），不静默继续。
case "$BASELINE_DAYS" in
  ''|*[!0-9]*) echo "[$(date '+%F %T')] BASELINE_DAYS 必须是正整数，收到: $BASELINE_DAYS" >&2; exit 64 ;;
esac
if [ "$BASELINE_DAYS" -le 0 ]; then
  echo "[$(date '+%F %T')] BASELINE_DAYS 必须是正整数，收到: $BASELINE_DAYS" >&2
  exit 64
fi

# 语句超时。★ 本脚本要跑的聚合是**重量级**的（8 天 ≈ 30,311,927 行），
#   而应用角色 llm_gateway 在库上带 statement_timeout=30s（应用侧保护）。
#   ⇒ 必须在自己的会话里放宽，否则每次都超时失败。
: "${STMT_TIMEOUT:=1800s}"

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }

de() {
  # 回显走 stderr：`x=$(de ...)` 会把 stdout 收进变量，回显混进去会污染取值。
  if [ -n "${PRINT_SQL:-}" ]; then printf '%s\n' "$1" >&2; printf '0\n'; return 0; fi
  # ★★★ 【客户端侧失败必须看得见】—— 2026-10-22 12h 审计轮修掉的一处真缺陷。
  #
  #   原来的形态：`... 2>&1 || true` ⇒ **psql 的退出码被丢弃**。
  #   而下面的 sql_failed() 只认**服务端** SQL 错误（输出里有 ERROR/FATAL 行）。
  #   ⇒ 客户端侧的失败**根本不会产生那种文本**，于是全部漏判：
  #       · 连接失败      → "psql: error: connection to server … failed"
  #       · podman 不可用 → "Error: unable to connect to Podman socket"
  #       · 命令不存在    → "bash: podman: command not found"
  #   ⇒ 这三种都被当成「跑完了」，然后
  #       log "refreshed rows=${out}" 把**错误文本当成刷新行数**打进日志。
  #   ⇒ ★★ 这正是本目录同一族脚本在巡检侧写下的判词：
  #        「量具坏了却在报绿」—— 最危险的一种故障形态。
  #   ⇒ 修法：**保留退出码**，并把「退出码≠0」翻译成与服务端错误**同形**的
  #     标记（ERROR: 前缀）⇒ 下游既有的 sql_failed() 判据一字不改就能判出来。
  #     ★ 为什么翻译成同形而不是改所有调用点：本文件有 4 个调用点，
  #       每个都是 `x=$(de …)` 形态；在 de() 里翻译 ⇒ 一处改动覆盖全部，
  #       且**不改变任何调用点的语义**（失败仍然是失败，只是终于被看见）。
  local out rc=0
  # shellcheck disable=SC2086 # PSQL_CMD 刻意按多词命令传入（与巡检脚本同款）
  out=$( ${PSQL_CMD:-podman exec -e "PGOPTIONS=-c statement_timeout=${STMT_TIMEOUT}" \
    "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAX -c} "$1" 2>&1 ) || rc=$?
  if [ "$rc" -ne 0 ]; then
    out="ERROR: psql exited ${rc}: ${out}"
  fi
  printf '%s' "$out"
  return 0
}

# 任一行以 ERROR/FATAL 开头 ⇒ psql 整条挂掉（与巡检脚本同款判据）。
sql_failed() { printf '%s\n' "$1" | grep -qE '^\s*(ERROR|FATAL)'; }

# === 1. 量具自检：函数在不在 ================================================
# ★ 这一步必须先做：迁移 892 没部署 ⇒ 函数不存在 ⇒ 刷新无从谈起。
#   而「静默跳过」正是这类作业最坏的失败形态（汇总表停更 ⇒ 巡检失明）。
fn_probe=$(de "SELECT coalesce(to_regprocedure('public.refresh_ursm_snapshot_slot_daily(integer, text)')::text, '')")
if sql_failed "$fn_probe" || [ -z "$fn_probe" ]; then
  log "FATAL: 缺少 public.refresh_ursm_snapshot_slot_daily(integer, text) ⇒ 迁移 892 未部署 ⇒ 汇总表无法刷新"
  log "FATAL: 巡检会因此【静默失明】（快路读到空基线 ⇒ 全部桶 NO-BASELINE）⇒ 请先部署迁移 892"
  exit 3
fi
log "start ursm-snapshot-slot-refresh granularity=${GRANULARITY} baseline_days=${BASELINE_DAYS} now=$(date '+%F %T')"

# === 2. 覆盖探测：窗口内已有几天 ===========================================
# ★ 与巡检脚本的探针**同口径**（同一段 SQL 的 count(DISTINCT day)）。
#   两边必须一致，否则会出现「刷新脚本以为够了、巡检脚本以为不够」的错位。
cov_sql="SELECT count(DISTINCT day)::text FROM public.ursm_snapshot_slot_daily
          WHERE granularity = '$GRANULARITY'
            AND day >= ((now() AT TIME ZONE current_setting('TimeZone'))::date
                        - make_interval(days => $BASELINE_DAYS))::date
            AND day <  (now() AT TIME ZONE current_setting('TimeZone'))::date"
cov_before=$(de "$cov_sql")
if sql_failed "$cov_before" || [ -z "$cov_before" ]; then
  log "FATAL: 覆盖探测失败：$cov_before"
  exit 3
fi
log "coverage_before=${cov_before}/${BASELINE_DAYS} days"

# === 3. 选补多少天 ==========================================================
# 稳态补 2 天（昨天 + 前天的迟到行）；覆盖不足时补满基线窗 + 1 天（自愈）。
if [ "$cov_before" -ge "$BASELINE_DAYS" ] 2>/dev/null; then
  DAYS_BACK=2
  REASON="incremental"
else
  DAYS_BACK=$((BASELINE_DAYS + 1))
  REASON="repair"
fi
log "plan days_back=${DAYS_BACK} reason=${REASON}"

# === 4. 刷新 ================================================================
# ★ 函数是幂等的（ON CONFLICT DO UPDATE）⇒ 失败重跑安全。
#   返回值 = 受影响行数（插入 + 更新）。
out=$(de "SELECT public.refresh_ursm_snapshot_slot_daily(${DAYS_BACK}, '${GRANULARITY}')::text")
if sql_failed "$out"; then
  log "FATAL: 刷新失败（days_back=${DAYS_BACK}）：$out"
  exit 3
fi
log "refreshed rows=${out} days_back=${DAYS_BACK} reason=${REASON}"

# === 5. 复核：覆盖是否真的到位 ==============================================
# ★ 必须**复核**而不是假定成功：刷新函数可能返回 0 行（例如源表当天没数据）
#   ⇒ 那时覆盖仍不足 ⇒ 必须让退出码如实反映（1），而不是报绿。
cov_after=$(de "$cov_sql")
if sql_failed "$cov_after" || [ -z "$cov_after" ]; then
  log "FATAL: 刷新后复核失败：$cov_after"
  exit 3
fi
log "coverage_after=${cov_after}/${BASELINE_DAYS} days"

if [ "$cov_after" -ge "$BASELINE_DAYS" ] 2>/dev/null; then
  log "OK: 汇总表覆盖完整（${cov_after}/${BASELINE_DAYS} 天）⇒ 巡检可走快路"
  exit 0
fi
log "WARN: 刷新后覆盖仍不足（${cov_after}/${BASELINE_DAYS} 天）⇒ 巡检会回退源表路（慢但不失明）"
exit 1
