#!/usr/bin/env bash
# monitor_single_instance_gate_test.sh —— 252-monitor 巡检「单实例闸门」回归门。
#
# ## 它守的是什么
#
# 2026-10-10 的生产事故：154 网关持续 503，日志里刷满
# `SQLSTATE 57P03 (the database system is not yet accepting connections)`。
# 顺着网关的 DSN 找到 252（172.16.2.210），现场是：
#
#   · load average 278~304，机器只有 4 核
#   · 2915 个进程，其中 **310 份 `pg17-disk-watch.sh` 副本**同跑
#   · **149 个 `docker exec pg-252-pg17 psql` 僵死**，最早的已挂 6.7 小时
#   · 内存 15G 用满、**swap 4095/4095 100% 占满**
#   · 11 个 `du` 卡在 D 状态（不可中断 IO）
#
# 成因链（每一步都在仓库里可查）：
#
#   1. PG「半死」——容器还在 listen，但事务卡死。
#   2. `pg17-disk-watch.sh` 的 `de()` 直接 `docker exec ... psql`，
#      **既无 statement_timeout 也无 connect_timeout**，于是 psql 一直挂着。
#      同一份脚本每轮还跑一次 `du -s /var/lib/containers/storage/`（全量遍历）。
#   3. `etc.cron.d.pg17` 里它是 `*/10 * * * *`——cron 的下一次触发照常发生，
#      而上一轮永远不会结束。**进程只增不减。**
#   4. 310 份副本 × (1 个 docker exec + 1 次全量 du) = 内存耗尽 + IO 风暴。
#   5. PG 因此无法服务，154 网网关拿到 57P03 ⇒ 一小时 194 个 HTTP 503。
#
# 结论：**巡检把被巡检对象压垮了**。在 PG 最需要被观察的时刻，
# 恰恰是最不能多跑的时刻。
#
# 所以本门断言的不是「脚本写得对」，而是三件可机械判定的事：
#   · 每个 cron 调度的巡检脚本都有单实例闸门（最多一份副本在跑）
#   · 闸门用 `flock -n`（非阻塞）而不是阻塞 flock
#   · 闸门不可用时 **fail-open**，不会反过来让监控静默
#   · 最关键的那份脚本（disk-watch）确实带上了查询超时（**一定会结束**）
#
# 只有前三条是不够的：单实例 + 无超时 = 那唯一的一份副本挂到天荒地老，
# 巡检变成一个永久僵尸。所以 pg17-disk-watch.sh 的 statement_timeout
# 同样是门的一部分。
#
# ## 为什么单独一个文件
#
# 零外部依赖（只要 bash；行为断言需要 flock），因此在任何 runner
# （含 macOS 开发机）都能跑完静态部分。与 252-monitor 的部署方式
# （整文件覆盖到 /opt/scripts/）无耦合，不会因为少传一个文件而失效。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MON="$ROOT/scripts/252-monitor"
CRON="$MON/etc.cron.d.pg17"

pass() { printf 'PASS: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

[ -d "$MON" ] || fail "找不到 $MON"
[ -f "$CRON" ] || fail "找不到 ${CRON}（cron 是整文件覆盖契约的 SSOT）"

# ---------------------------------------------------------------- 静态门

# 1. cron 里调度到的每个巡检脚本，都必须有单实例闸门。
#
#    判据取自 cron 文件本身而不是一张手写清单：手写清单会漏，
#    而 cron_registration_test.go 已经踩过同一个坑（它断言 cron 行有对应
#    脚本，却管不到脚本**有没有自保**）。
scheduled=()
while IFS= read -r name; do
  scheduled+=("$name")
done < <(grep -oE '/opt/scripts/[A-Za-z0-9._-]+\.sh' "$CRON" | sed 's#.*/##' | sort -u)

[ "${#scheduled[@]}" -gt 0 ] || fail "$CRON 里没有解析出任何被调度的脚本 —— 判据本身失效了"

for name in "${scheduled[@]}"; do
  path="$MON/$name"
  [ -f "$path" ] || continue  # 「cron 指向不存在的脚本」由 cron_registration_test.go 负责
  # 判据取**闸门的真实签名**（`flock -n 9`），而不是闸门里出现的变量名：
  # 变量名还会出现在采样日志行里，把整段闸门整段删掉之后 grep 依然匹配得上
  # —— 那样这道门会对「闸门没了」完全失明。第一版正是这么写的，
  # 变异验证时被抓出来才改。
  #
  # ★ 必须是**非阻塞**的 `flock -n`：阻塞式 flock 的后果恰好和想要的
  #   效果相反 —— cron 的每一轮都会乖乖排队等锁，每 10 分钟多一个
  #   等待中的进程，只是从「各自乱跑」换成「整齐地堆积」。
  #   抢不到锁应当**立即跳过本轮**。
  grep -q 'flock -n 9' "$path" \
    || fail "$name 被 etc.cron.d.pg17 调度，却没有单实例闸门（未见 flock -n 9）。
   ★ 这正是 2026-10-10 事故的形状：cron 每 10 分钟拉一个副本，
     上一轮因 psql 挂死永远不结束，进程只增不减（现场 310 份）。
     给新脚本加 cron 行时，同步加这段闸门。"
done
pass "所有被 cron 调度的巡检脚本都带**非阻塞**单实例闸门（${#scheduled[@]} 份）"

# 2. 闸门必须 fail-open：`command -v flock` 缺失时放行，而不是让脚本失败。
#
#    反过来做（拿不到锁就退出非 0）的话，开发机 / 异常机器上
#    整个巡检目录会集体报错退出 —— 监控静默比重复采样更糟，
#    而且静默的监控不会被任何人发现已经坏了。
for name in "${scheduled[@]}"; do
  path="$MON/$name"
  [ -f "$path" ] || continue
  grep -q 'flock -n 9' "$path" || continue
  grep -q 'command -v flock' "$path" \
    || fail "$name 的闸门没有 \`command -v flock\` 判据。
   ★ 没有这道判据，闸门自身不可用（无 flock / 锁目录建不了）时脚本会
     整体失败退出 —— 把「防堆积」变成「监控静默」。必须 fail-open。"
done
pass "闸门在自身不可用时 fail-open"

# 3. 心跳脚本的闸门必须排在 EXIT trap **之后**。
#
#    pg17-pg-availability-check.sh 用 EXIT trap 写
#    /var/lib/pg17-check/pg-availability.lastrun，那是「巡检还在跑吗」的判据
#    （健康时它刻意不输出，所以只能问 lastrun 的 mtime）。
#    闸门若排在 trap 之前，抢锁失败直接 exit 0 会让 lastrun 不更新 ——
#    运维据此会误判「心跳已经停了」，而它其实好好地在跑。
hb="$MON/pg17-pg-availability-check.sh"
if [ -f "$hb" ] && grep -q '_llmgw_single_instance_guarded' "$hb"; then
  trap_line=$(grep -n "EXIT" "$hb" | head -1 | cut -d: -f1)
  gate_line=$(grep -n '_llmgw_single_instance_guarded=false' "$hb" | head -1 | cut -d: -f1)
  [ -n "$trap_line" ] && [ -n "$gate_line" ] || fail "$hb 找不到 EXIT trap 或闸门起始行"
  [ "$gate_line" -gt "$trap_line" ] \
    || fail "$hb 的闸门（行 ${gate_line}）排在了 EXIT trap（行 ${trap_line}）**之前**。
   ★ 抢锁失败时脚本会 exit 0，EXIT trap 是唯一会写 lastrun 的地方 ——
     闸门排在前面，lastrun 就不更新，运维会误判心跳已停。"
  pass "心跳脚本的闸门排在 EXIT trap 之后（trap@$trap_line < gate@${gate_line}）"
fi

# 4. 磁盘巡检必须带查询超时 —— 单实例闸门的另一半。
#
#    只有闸门没有超时：最多一份副本，但它可以永远挂着。事故当天
#    149 个僵死的 `docker exec ... psql` 就是「已经有一份了，于是不再新增」
#    之前的原始形态。⇒ 两道闸缺一不可。
dw="$MON/pg17-disk-watch.sh"
[ -f "$dw" ] || fail "找不到 $dw"
grep -q 'statement_timeout' "$dw" \
  || fail "pg17-disk-watch.sh 没有 statement_timeout。
   ★ psql 会在「PG 半死」时一直挂着：有单实例闸门也只是把 310 份
     变成 1 份僵尸而已。这道门保证它**一定会结束**。"
grep -q 'connect_timeout\|PGCONNECT_TIMEOUT' "$dw" \
  || fail "pg17-disk-watch.sh 没有连接超时 —— 连不上 PG 时同样会一直挂着。"
pass "pg17-disk-watch.sh 同时具备单实例闸门与查询/连接超时"

# ---------------------------------------------------------------- 行为门

if ! command -v flock >/dev/null 2>&1; then
  # macOS 默认没有 flock。本仓库的既有约定是：行为断言只在量具可用时跑，
  # 静态门在任何 runner 上都要跑。开发机上缺 flock 不是缺陷。
  printf 'NOTE: 本机无 flock（macOS 自带 bash 3.2），仅跳过 6/7/8 三条互斥行为断言。\n'
  printf '      其余静态门与第 9 节的量具不可用/孤儿回收断言**仍然执行**。\n'
  HAVE_FLOCK=0
else
  HAVE_FLOCK=1
fi

# 行为断言用的是**脚本里那段闸门的真实文本**，不是复制的副本 ——
# 复制的副本会随脚本演进悄悄对不上，门就变成测自己而不是测产物。
#
# ★ 抽取边界的两个坑（都踩过）：
#   1) `=true` 那行在脚本里是**缩进**的，所以 `/^_llmgw_...=true$/` 匹配不上，
#      done_marker 永远不置位，抽取会一路吃到文件末尾 —— 于是「探针」里
#      混进了整个脚本主体，行为断言测的是一个完全不同的东西。
#      ⇒ 两处正则都必须允许前导空白。
#   2) 抽多了不会有任何报错，只会安静地测错东西。所以下面有断言 0：
#      抽出来的段里**不许**出现主体的标志（采样 SQL），
#      否则宁可失败也不能让它继续测下去。
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

awk '
  /# === 单实例闸门/ && !seen { seen=1; cap=1 }
  seen && cap {
    print
    if (done_marker && $0 ~ /^[[:space:]]*fi[[:space:]]*$/) cap=0
    if ($0 ~ /_llmgw_single_instance_guarded=true/) done_marker=1
  }
' "$dw" > "$tmp/gate.txt"

gate_block=$(cat "$tmp/gate.txt")
[ -n "$gate_block" ] || fail "无法从 ${dw} 抽出单实例闸门代码段（标记是否被改动过？）"

# 判据 0（自证）：抽出来的必须**只是**闸门。
# 一旦抽取越界，这里必须响 —— 否则行为断言会安静地去测一个错误的东西，
# 而「门全绿」恰恰是这类错误最常见的表现。
case "$gate_block" in
  *pg_database_size*|*CONF=*|*"mkdir -p \"\$(dirname"*)
    fail "从 ${dw} 抽出的闸门代码段越界了（混进了脚本主体）。
   ★ 这会让下面的行为断言测错东西却仍然全绿。抽取正则是
     \`/# === 单实例闸门/\` 起、\`_llmgw_single_instance_guarded=true\` 后的
     第一个 fi 止；脚本里该行是缩进的，正则必须允许前导空白。" ;;
esac
grep -q 'flock -n 9' "$tmp/gate.txt" \
  || fail "抽出的闸门段里没有 \`flock -n 9\` —— 抽取边界不对。"

# 探针 = 真实闸门 + 一个「进入主体」的标记 + 一个短暂睡眠。
# 睡眠是为了制造重叠窗口：没有它，N 个并发可能各自持锁进入，测不出问题。
#
# ★ 用**文件拼接**而不是插值 heredoc：
#   闸门文本里有 `$0`、`${LLMGW_LOCK_DIR:-...}`、`$(basename ...)`。
#   放进未加引号的 heredoc 会在**建探针时**就被展开掉（那些变量在测试
#   自己的 shell 里多半未绑定），探针于是变成另一个东西 —— 而且不报错。
#   所以闸门文本按原样落盘再拼接，主体只通过环境变量取路径。
cat > "$tmp/body.sh" <<'BODY'
echo enter >> "$RUNS_FILE"
sleep 3
BODY

{
  printf '%s\n' '#!/usr/bin/env bash'
  printf '%s\n' 'set -euo pipefail'
  cat "$tmp/gate.txt"
  cat "$tmp/body.sh"
} > "$tmp/probe.sh"
chmod +x "$tmp/probe.sh"

# 让闸门写到临时目录，避免污染真实 /var/run/lock。
export LLMGW_LOCK_DIR="$tmp/lock"
export LLMGW_LOCK_SKIP_LOG="$tmp/skip.log"
RUNS_FILE="$tmp/runs"
export RUNS_FILE

# 6~8 三条依赖 flock 的互斥行为断言（无 flock 的机器上跳过，但**不是**直接
# exit 掉整个门——否则第 9 节那些不需要 flock 的断言会跟着一起不执行，
# 变成「看着有门、其实从没跑过」。这正是本门第一版的毛病。
if [ "$HAVE_FLOCK" = "1" ]; then

# 6. N 份并发 → 恰好 1 份进入主体。
#
#    2026-10-10 的现场是 310 份副本。这里用 8 份：足以复现重叠，
#    又不至于让一台开发机自己变慢。
N=8
pids=()
for _ in $(seq 1 "$N"); do
  bash "$tmp/probe.sh" &
  pids+=($!)
done
for p in "${pids[@]}"; do wait "$p" || true; done

entered=$(wc -l < "$RUNS_FILE" 2>/dev/null || echo 0)
entered=${entered// /}
[ "$entered" = "1" ] \
  || fail "8 份并发里有 $entered 份进入了主体（期望恰好 1）。
   ★ 这正是事故的形状：副本互相踩踏，闸门没生效。
     注意 N>1 是**允许**的（fail-open 路径），但本机有 flock，
     闸门应当可用，所以必须恰好 1。"
pass "8 份并发中恰好 1 份进入主体（其余 7 份立即跳过）"

# 7. 跳过的那几份必须快速退出，而不是排队等锁。
#
#    行为门的一部分：闸门是 `flock -n` 的话，被跳过的进程应该
#    在闸门那一行就返回，而不是在 sleep 里等 3 秒。
#
#    直接在本 shell 里跑，不套 `bash -c '...'`：嵌套单引号里再插
#    $tmp 是这类脚本最容易写错的地方，写错时往往还是「能跑、只是没测到」。
#    ★ 只量「被跳过的那一份」自己花了多久，**不能**把 holder 的
#      `wait` 算进去 —— holder 本来就要 sleep 3 秒，计进去的话
#      这道断言永远失败，而且失败原因与闸门无关（第一版就栽在这里：
#      门报的是「在排队等锁」，实际量的是持锁那一份的正常时长）。
bash "$tmp/probe.sh" >/dev/null 2>&1 &
holder=$!
sleep 1
t0=$SECONDS
bash "$tmp/probe.sh" >/dev/null 2>&1
skipped_rc=$?
t1=$SECONDS
wait "$holder" || true
skipped_elapsed=$((t1 - t0))

[ "$skipped_rc" -eq 0 ] \
  || fail "抢不到锁的那份退出码是 ${skipped_rc}，期望 0 —— 跳过是正常路径，不是故障。
   ★ 报非 0 会让 cron 把这一轮读成故障，而本目录契约里 3 还会触发假的「巡检失效」告警。"
[ "$skipped_elapsed" -lt 3 ] \
  || fail "被跳过的副本耗时 ${skipped_elapsed}s，说明它在排队等锁而不是立即跳过。
   ★ 阻塞 flock 的后果：cron 每 10 分钟多一个等待进程 —— 乱堆换成整齐地堆。"
pass "抢不到锁的副本立即以 0 退出（不排队、不报错）"

# 8. 上一轮结束后锁必须被释放（没有永久占用）。
#
#    如果闸门用了错误的 fd 或把锁写死在别处，探针会永远拿不到锁，
#    于是巡检从「偶尔跑」变成「再也不跑」——比堆积更难发现。
rm -f "$RUNS_FILE"
bash "$tmp/probe.sh" >/dev/null 2>&1
rc=$?
[ "$rc" -eq 0 ] || fail "前一轮结束后再跑一次退出码 ${rc}，锁疑似被永久占用"
entered=$(wc -l < "$RUNS_FILE" 2>/dev/null || echo 0)
entered=${entered// /}
[ "$entered" = "1" ] \
  || fail "前一轮结束后本轮没有进入主体（进入 $entered 次，期望 1）—— 锁泄漏了"
pass "上一轮结束后锁正常释放，下一轮可以正常进入"

fi  # HAVE_FLOCK — 6/7/8 结束

# =====================================================================
# 9. 量具不可用必须与「一切正常」分开（2026-10-11 审计新增）
#
# ★ 这道门是被本次审计自己的发现逼出来的，不是预防性加的。
#   事故当天全天 psql 一条都取不到，而采样行始终是 `level=ok`——
#   因为 `de()` 失败返回 "0"，而每条阈值判断都是「>= 阈值」，
#   **取不到值**与**一切正常**在判据里完全同形。
#   ⇒ 监控在完全瞎掉的状态下报了 8 小时健康，而那 8 小时正是
#     154 被 Redis/PG 拖垮、无人能从监控侧发现的时间窗。
#   本目录契约里 3 = 量具不可用，这里就必须落到 gauge_unavailable。
# =====================================================================

# 9.1 静态：必须存在量具可用性判定，且失败计数走**文件**而非变量。
#
#     走变量是错的：调用方一律 `x=$(de ...)`，那是子 shell，自增出不来
#     （第一版就是这么写的，失败计数恒为 0，等于没记）。
#
# ★ 所有静态断言都只匹配**代码行**，必须先剥掉整行注释。
#   第一版直接 grep 整个文件，立刻翻车：断言自己在解释性注释里写了
#   `du -s /var/lib/containers/...` 和 `${ts:0:7}` 这两个「反面教材」字符串，
#   于是脚本明明已经改对了，门却报 FAIL。
#   ⇒ 一道会被自己的注释触发的门，等于没有门。
#   （已知局限：只剥整行注释，行尾注释仍会被匹配；本目录脚本的注释都是整行。）
dw_code=$(grep -vE '^[[:space:]]*#' "$dw")

case "$dw_code" in
  *gauge_unavailable*)
    : ;;
  *)
    fail "pg17-disk-watch.sh 没有 gauge_unavailable 判定。
   ★ 没有它，取不到值与一切正常同形：监控瞎掉也会报 level=ok。" ;;
esac
case "$dw_code" in
  *_DE_STAT_FILE*) : ;;
  *)
    fail "pg17-disk-watch.sh 的 de() 失败计数没有走文件。
   ★ \`x=\$(de ...)\` 是子 shell，变量自增出不来，计数必须落盘。" ;;
esac
case "$dw_code" in
  *"alerts=()"*|*"alerts+="*) : ;;
  *)
    fail "pg17-disk-watch.sh 里找不到 alerts 数组，无法承载量具不可用告警。" ;;
esac

# 9.2 静态：不得再有全量遍历容器存储的 du。
#
#     实测 252 上 `du -s /var/lib/containers/storage/` **跑 280 秒仍未返回**。
#     它本就是事故当天两个负载源之一（现场 11 个 du 卡 D 状态）。
case "$dw_code" in
  *"du -s /var/lib/containers"*)
    fail "pg17-disk-watch.sh 又出现了 du -s /var/lib/containers。
   ★ 实测该命令在 252 上跑 280 秒仍未返回；事故当天现场 11 个 du 卡在
     D 状态，它就是两个负载源之一。给它加超时只会把指标**永久打瞎**
     （采样行长期 n/a），比慢但正确更糟——请用 df -P 取所在文件系统用量。" ;;
  *) : ;;
esac

# 9.3 静态：不得引用未赋值的 ${ts:...}（曾在生产 bash 4.4 上崩掉告警通路）。
case "$dw_code" in
  *'${ts'*)
    fail "pg17-disk-watch.sh 引用了 \${ts:...}，但脚本里从未给 ts 赋值。
   ★ 已在 252 实测：bash 4.4 + set -u 会报 \`ts: unbound variable\` 并退出 1，
     后果是**任何一次真告警都在最后一步崩掉、IM 永远推不出去**。" ;;
  *) : ;;
esac

# 9.4 行为：伪容器下必须报量具不可用，且退出码为 3。
#
#     这是**端到端**跑真脚本，不是 grep。它同时覆盖了：
#       - 「一条阈值都没触发」时脚本不得崩（bash 3.2 空数组展开曾使其当场退出）
#       - gauge 失败计数确实能被数出来（子 shell 文件计数是否真的生效）
#       - top5 / dead_tup 不得谎称 clean
bdir=$(mktemp -d)
LLMGW_LOCK_DIR="$bdir/lock" \
LLMGW_DISK_WATCH_LOG="$bdir/dw.log" \
LLMGW_DISK_WATCH_ALERT_LOG="$bdir/dw.alert.log" \
LLMGW_PG_CONTAINER="definitely-not-a-container" \
LLMGW_QUERY_TIMEOUT=3 \
  bash "$dw" >/dev/null 2>&1
dw_rc=$?
[ "$dw_rc" -eq 3 ] \
  || fail "伪容器下 pg17-disk-watch.sh 退出码 ${dw_rc}，期望 3（量具不可用）。
   ★ 若为 0 就是「取不到值被当成一切正常」——这正是 10-10 那天报错了 8 小时的状态。"
grep -q 'level=gauge_unavailable' "$bdir/dw.log" 2>/dev/null \
  || fail "伪容器下采样行没有 level=gauge_unavailable。实际内容：
$(cat "$bdir/dw.log" 2>/dev/null)"
grep -qE 'gauge=[1-9][0-9]*/' "$bdir/dw.log" 2>/dev/null \
  || fail "采样行里 gauge 计数为 0，说明 de() 的失败没有被记下来：
$(cat "$bdir/dw.log" 2>/dev/null)"
if grep -q 'dead_tup_top3: clean' "$bdir/dw.log" 2>/dev/null; then
  fail "伪容器下 dead_tup_top3 显示 clean —— 查不到时被谎称为健康。"
fi
pass "伪容器下报 gauge_unavailable + 退出码 3（查不到 ≠ 没问题）"

# 9.5 行为：超时必须按进程组回收，不能留下孙进程。
#
#     ★ 这道门对应审计实测出的真 bug：`kill -9 <直接子进程>` 之后
#       `bash -c 'sleep 45; …'` 里那条 sleep **会活下来**，连调 3 次漏 3 个。
#       「超时的进程留下来继续跑」正是 2026-10-10 事故的形态——
#       超时这道闸若自己漏进程，等于把事故原样复现。
#
#     ★★ 第一版这里是**空转**的（变异验证当场抓出来）：它写的是
#       `eval "$wd_fn"' 2 bash -c "..."'`。eval 不会改变位置参数，
#       于是函数体里的 `local secs="$1"` 读到的是**外层脚本**的 $1，
#       看门狗根本没被执行 → 恒为「零孤儿」→ 这条断言永远绿。
#       ⇒ 改为把函数落到真实脚本文件里、用真实参数调用。
if [ -f "$tmp/gate.txt" ]; then
  wd_fn=$(awk '/^_llmgw_run_bounded\(\) \{/,/^\}/' "$dw")
  if [ -n "$wd_fn" ]; then
    {
      printf '%s\n' '#!/usr/bin/env bash' 'set -uo pipefail'
      printf '%s\n' "$wd_fn"
      # 注意不能用 `|| true`：那会把看门狗的 124 吞成 0，自证就永远失败。
      # 这里显式把函数的返回码作为脚本退出码传出去。
      printf '%s\n' '_llmgw_run_bounded 2 bash -c "sleep 45; date > /dev/null"'
      printf '%s\n' 'exit $?'
    } > "$tmp/wd_run.sh"
    chmod +x "$tmp/wd_run.sh"

    # 先自证这扇门真的会跑：正常路径应当在 2~4s 内自己结束并返回 124。
    probe_s=$(date +%s)
    bash "$tmp/wd_run.sh" >/dev/null 2>&1
    probe_rc=$?
    probe_e=$(date +%s)
    [ "$probe_rc" -eq 124 ] \
      || fail "看门狗探针自证失败：返回码 ${probe_rc}，期望 124。
   ★ 若探针根本没执行，本节后续的「零孤儿」就是**空转的绿**——
     必须先证明探针会跑，结论才有意义。"
    [ $((probe_e - probe_s)) -le 8 ] \
      || fail "看门狗探针耗时 $((probe_e - probe_s))s，说明超时上界没生效"

    base_sleep=$(pgrep -x sleep 2>/dev/null | wc -l | tr -d ' ')
    for _ in 1 2 3; do bash "$tmp/wd_run.sh" >/dev/null 2>&1; done
    sleep 2
    after_sleep=$(pgrep -x sleep 2>/dev/null | wc -l | tr -d ' ')
    leak=$((after_sleep - base_sleep))
    [ "$leak" -le 0 ] \
      || fail "看门狗超时后留下 $leak 个孙进程（期望 0）。
   ★ 只 kill 直接子进程会漏掉 \`bash -c 'sleep …'\` 的 sleep。
     事故当天「进程只增不减」就是这个形态：超时闸自己漏进程。"
    pass "看门狗按进程组回收，连调 3 次零孤儿（探针已自证确实在跑）"
  fi
fi

rm -rf "$bdir"

printf '\nOK: monitor_single_instance_gate_test 全部通过\n'
exit 0