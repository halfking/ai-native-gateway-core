#!/usr/bin/env bash
# pg17-pg-availability-check.sh —— PG 可用性心跳巡检（2026-10-05 新增）
#
# 存在的原因是一次真实事故：2026-10-05 15:46 生产容器 pg-252-pg17 被误删，
# 15:49:41 才恢复，停机约 3~4 分钟。**整个过程没有任何告警。**
#
# ★ 为什么既有巡检一个都抓不到（这决定了本脚本的设计，必须先想清楚）：
#   · pg17-disk-watch.sh   每 10 分钟，看的是**磁盘**。磁盘一直健康。
#   · pg17-emergency-*      每 15 分钟，是 `--auto` 的**磁盘 90% 应急**。
#   · pg17-vacuum-bloat.sh / pg17-index-bloat.sh   **周级**的，慢 2~3 天。
#   · pg-table-bloat-check.sh  **每天 05:07** 的全库普查。它确实会在那天早上
#     报「量具不可用」，但那已经是 13 小时之后 —— 而 PG 只停了 4 分钟。
#   ⇒ 缺的不是第七个「体检」脚本，是**心跳**：一分钟问一次「活着吗」。
#     巡检频率必须匹配故障的检测目标；本目录之前把这两件事当成了一件事，
#     于是有 8 个「体检」却没有 1 个「心跳」。
#
# 退出码（与本目录同一套契约）：
#   0  PG 可用且可写
#   1  PG 不可用（容器缺失 / 未运行 / 连不上 / 一直在 recovery / 只读）
#   3  本次没有结论（量具不可用：容器运行时都找不到、状态目录建不了、
#                    或 psql 成功但输出格式漂移）
#   ★ 3 与 0 必须可区分：「查不到」不是「没问题」。这是本目录的既有教训，
#     pg-table-bloat-check.sh 曾把自己违反过一次（查成功且零行被报成量具坏了）。
#
# 去重：每分钟跑一次，若 PG 挂 30 分钟而无去重就是 30 条告警 ——
# 而告警一旦变成噪音，看告警的人就会开始无视它，那比不告警更糟。
# ⇒ 状态文件记住「何时开始不可用」，只在**首次**与**恢复**这两个转换点推送。
#
# 日志约定：**健康时不输出**。每分钟一条 OK 只会把真事件淹掉。
# 「巡检还在跑吗」问 lastrun 文件的 mtime，不要问日志里有没有新行。

set -uo pipefail

CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-llm_gateway}
PG_DB=${PG17_DB:-llm_gateway}
# ★ 锁超时时：PG 半死不活（还在接受连接但事务卡死）时 psql 会**一直挂着**，
#   cron 的下一次触发与这一轮重叠，进程越堆越多，最后把机器吃满 ——
#   「为了发现 PG 挂了而把机器搞挂」是比停机更坏的结果。
QUERY_TIMEOUT=${PG17_QUERY_TIMEOUT:-10}
NOTIFY=${PG17_NOTIFY:-/opt/scripts/notify.sh}
STATE_DIR=${PG17_STATE_DIR:-/var/lib/pg17-check}
STATE_FILE="$STATE_DIR/pg-availability.state"
LAST_RUN="$STATE_DIR/pg-availability.lastrun"

# 心跳：无论走哪条退出路径都写一次。**巡检自己挂了就该能从这个文件的
# mtime 看出来**，而不是从「日志里没有新行」猜 —— 后者与「一切正常」同形。
#
# ★ 整条重定向放进 `{ ...; } 2>/dev/null`：目录不存在时是**重定向本身**失败，
#   bash 会在 trap 里打一行错误。`cmd > f || true` 压不住它 ——
#   错误发生在建立重定向的时刻，早于 || 能观察的位置。
#   （第一版就是这么写的，于是「量具不可用」这条消息旁边永远跟着一行
#     `行 1: ...lastrun: No such file or directory`，把真正要读的那行挤下去。）
trap '{ date +%Y-%m-%dT%H:%M:%S%z > "$LAST_RUN"; } 2>/dev/null || true' EXIT

log() { printf '%s %s\n' "$(now_iso)" "$*" >&2; }

# ★ 这里**不能**用 `date -Is`：那是 GNU coreutils 的写法，BSD/macOS 的 date
#   不认（报 "date: invalid argument 's' for -I"），于是每一行时间戳都变成空。
#   本目录其余脚本仍写 `date -Is`，它们不因此出错是因为没有任何门会去读
#   它们的时间戳 —— 而本脚本**有**门（monitor_script_exec_gate_test.go）
#   在开发机（macOS）上实跑。
#   ⇒ 那么在开发机上被测的就不是部署到 252 上跑的那份代码。
#     判据的测量面必须和部署面一致，否则这道门测的是「我本机的 date」。
now_iso() { date +%Y-%m-%dT%H:%M:%S%z; }

# ------------------------------------------------------------------ 状态与通知
# state 文件一行三列：<状态>\t<该状态起点的 epoch>\t<原因>
#   状态 ∈ up | down | unknown
#
# ★ 为什么有第三种 unknown：量具失效时我们**不知道** PG 好不好。
#   若此时把状态写成 up，恢复时就会漏报一次故障；若写成 down，
#   恢复通知又会编出一个假的「中断 N 分钟」。如实记 unknown，
#   恢复时说「从 X 起状态不明、现已确认正常」—— 宁可少说，不可乱说。

read_state() {
  _S=up; _SINCE=$(date +%s); _REASON=-
  if [ -f "$STATE_FILE" ]; then
    IFS=$'\t' read -r _S _SINCE _REASON < "$STATE_FILE" || true
    case "$_S" in up|down|unknown) ;; *) _S=up ;; esac
    case "$_SINCE" in ''|*[!0-9]*) _SINCE=$(date +%s) ;; esac
    [ -n "$_REASON" ] || _REASON=-
  fi
}

write_state() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" > "$STATE_FILE" 2>/dev/null || true; }

mins_since() { echo $(( ($(date +%s) - $1) / 60 )); }

notify() {
  local level=$1 title=$2 body=$3
  if [ ! -x "$NOTIFY" ]; then
    # 这正是事故当天缺的那一环：没有推送通道时，告警只活在 log 里。
    # 所以这里要喊出来，而不是静默。
    log "WARN: $NOTIFY 不可执行 —— 这次告警没有推送出去，只落在本脚本的 log 里"
    return 0
  fi
  "$NOTIFY" --level "$level" --title "$title" --body "$body" >/dev/null 2>&1 || \
    log "WARN: notify.sh 返回非零（它内部会退本地 log，故不影响判定）"
}

# PG 不可用。reason 供恢复通知引用，也供人一眼看出是哪一类故障。
verdict_down() {
  local reason=$1 msg=$2
  read_state
  if [ "$_S" = "down" ]; then
    # 已经在告警态：只更新原因与日志，**不重复推送**。
    write_state down "$_SINCE" "$reason"
    log "DOWN(持续 $(mins_since "$_SINCE") 分): $msg"
    return 0
  fi
  local was="首次"
  [ "$_S" = "unknown" ] && was="量具失效后转为不可用"
  write_state down "$(date +%s)" "$reason"
  log "DOWN($was): $msg"
  notify critical "PG 不可用：$CONTAINER" "$msg"
}

# 量具自己坏了。这是三态里最容易漏报的一种，所以措辞上不许含糊。
#
# ★ 这里也必须推一次告警（且只在**首次**推）：
#   「巡检坏了」正是 2026-10-05 那次事故的形态 —— 告警通道退化成
#   「什么都收得到但什么都不说」，事后与「完全没有通道」无法区分。
#   若量具失效只写日志不推送，那么监控体系里就会安静地少掉一双眼睛，
#   而没有任何东西会提到这件事。
verdict_gauge_down() {
  local msg=$1
  read_state
  log "ABORT: $msg"
  log "       ★ 这句话的含义是**巡检坏了**，不是「PG 没问题」。"
  log "         两者在本目录的退出码里分别是 3 和 0，不要读反。"
  if [ "$_S" = "unknown" ]; then
    log "       （持续 $(mins_since "$_SINCE") 分，不重复推送）"
    return 0
  fi
  write_state unknown "$(date +%s)" "gauge"
  notify critical "PG 可用性巡检失效：$CONTAINER" \
    "巡检自身无法给出结论，本次**没有**关于 PG 好坏的信息。
$msg
★ 在此期间 PG 是否可用是未知的，请不要把「没报 DOWN」读成「PG 正常」。"
}

# 健康。稳态静默：每分钟一条 OK 只会把真事件淹掉。
verdict_ok() {
  read_state
  if [ "$_S" = "down" ]; then
    local m; m=$(mins_since "$_SINCE")
    write_state up "$(date +%s)" "-"
    log "RECOVERED: PG 恢复，中断 $m 分钟（原因 $_REASON）"
    notify info "PG 已恢复：$CONTAINER" "中断 $m 分钟后恢复正常（原因：$_REASON）"
    return 0
  fi
  if [ "$_S" = "unknown" ]; then
    local m; m=$(mins_since "$_SINCE")
    write_state up "$(date +%s)" "-"
    log "RECOVERED: 状态不再不明（历时 $m 分），现已确认 PG 可用且可写"
    notify info "PG 已恢复：$CONTAINER" \
      "此前有 $m 分钟状态不明（巡检自身失效），现已确认可用。"
    return 0
  fi
  write_state up "$(date +%s)" "-"
}

# ---------------------------------------------------------------- 0. 量具自检
# 挑容器运行时。★ 252 上 `docker` 是指向 `/usr/bin/podman` 的符号链接
#   （/usr/local/bin/docker -> /usr/bin/podman），两个名字都能用。
#   本目录其他脚本一律写死 `docker exec`，pg17-start.sh 一律写 `podman exec`
#   —— 两边都对，但**没人写下这个事实**，于是它看起来像 bug。
#   这里按「谁真的找得到这个容器」来挑，而不是按哪个名字先出现在 PATH。
#   另一个理由：若哪天 docker 不再是软链而 PG 确实是 podman 管的，
#   写死 docker 的脚本会静默地 exec 到「另一个 docker 里的空环境」上。
RUNTIME=""
RUNTIME_FALLBACK=""
for rt in podman docker; do
  command -v "$rt" >/dev/null 2>&1 || continue
  if "$rt" inspect "$CONTAINER" >/dev/null 2>&1; then RUNTIME="$rt"; break; fi
  [ -n "$RUNTIME_FALLBACK" ] || RUNTIME_FALLBACK="$rt"
done
# 两个运行时都找不到这个容器时也得有名字可报：「PG 挂了」必须说清是
# 「容器没了」还是「找错了运行时」—— 这两者的处置动作完全不同。
[ -n "$RUNTIME" ] || RUNTIME="$RUNTIME_FALLBACK"

if [ -z "$RUNTIME" ]; then
  verdict_gauge_down "podman 与 docker 都不在 PATH 上，无法判断 PG 状态"
  exit 3
fi

# 状态目录建不了 ⇒ 无法去重 ⇒ 挂 30 分钟会推 30 条告警。
# 此时报 1 是在制造噪音，报 0 是在撒谎。选 3 并说清坏的是哪一件：
# 「量具的告警能力坏了」是需要被修掉的事实，不该被记成「PG 健康」。
if ! mkdir -p "$STATE_DIR" 2>/dev/null; then
  verdict_gauge_down "状态目录 $STATE_DIR 建不了，去重不可用"
  log "       后果：PG 故障期间每分钟会推一条告警，告警会退化成噪音。"
  exit 3
fi

# ---------------------------------------------------------------- 1. 容器状态
CSTATE=$("$RUNTIME" inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null)
if [ -z "$CSTATE" ]; then
  verdict_down "container_missing" "容器 $CONTAINER 在 $RUNTIME 下不存在"
  exit 1
fi
if [ "$CSTATE" != "running" ]; then
  # OOMKilled 的 exit code 是 137。把这几个字段带进告警，
  # 是为了让「PG 挂了」变成「PG 挂了 exit=137 OOMKilled=true」—— 可直接动手。
  DETAIL=$("$RUNTIME" inspect -f \
    'exit={{.State.ExitCode}} oom={{.State.OOMKilled}} finished={{.State.FinishedAt}}' \
    "$CONTAINER" 2>/dev/null)
  verdict_down "container_${CSTATE}" "容器 $CONTAINER 状态=$CSTATE（$DETAIL）"
  exit 1
fi

# ------------------------------------------------- 2. ★ 容器在跑 ≠ PG 能用
# 这一步不能省，也不能用别的方式代替。
#
# 事故当天我自己就撞上过这个形态：`nc 172.16.2.210 5432` 通，
# 但 PG 协议层握手被断 —— TCP 通、协议不通。
# 也就是说「端口开着」「容器 running」都**不是**可用性的证据，
# 唯一可信的证据是一条真的查询成功。
PSQL_OUT=$(timeout "$QUERY_TIMEOUT" "$RUNTIME" exec -i "$CONTAINER" \
  psql -U "$PG_USER" -d "$PG_DB" -X -q -A -t -F'|' \
  -v ON_ERROR_STOP=1 \
  -c "SELECT pg_is_in_recovery()::int || '|' || current_setting('transaction_read_only')" \
  2>&1)
rc=$?

if [ "$rc" -ne 0 ]; then
  # rc=124 是 timeout 自己的码：进程还挂着但问不出结果，单独点出来，
  # 因为它的含义是「PG 卡死」而不是「PG 拒连」，处置动作不同。
  if [ "$rc" -eq 124 ]; then
    verdict_down "query_timeout" \
      "容器在跑，但 ${QUERY_TIMEOUT}s 内 psql 没返回（PG 卡死，不是拒连）"
  else
    verdict_down "query_failed" \
      "容器在跑，但 psql 失败(rc=$rc): $(printf '%s' "$PSQL_OUT" | tr '\n' ' ' | cut -c1-200)"
  fi
  exit 1
fi

# 判据先看退出码、再看输出形状（R44：pg-table-bloat-check.sh 踩过「查成功
# 且零行 ⇒ 被当成量具坏了」；这里的形态是「查成功但格式漂移」）。
case "$PSQL_OUT" in
  '0|off')
    verdict_ok
    ;;
  '1|'*|'0|on')
    verdict_down "not_writable" \
      "能连上但不能服务写入：$(printf '%s' "$PSQL_OUT" | tr '\n' ' ')"
    exit 1
    ;;
  '')
    verdict_gauge_down "psql 成功但输出为空，判据无法解析"
    exit 3
    ;;
  *)
    verdict_gauge_down "psql 成功但输出格式漂移: $(printf '%s' "$PSQL_OUT" | tr '\n' ' ' | cut -c1-200)"
    log "       ★ 通常意味着 current_setting 的返回形态变了，而不是 PG 坏了。"
    exit 3
    ;;
esac

exit 0
