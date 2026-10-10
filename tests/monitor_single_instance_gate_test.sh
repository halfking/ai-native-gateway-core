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
  printf 'SKIP: 本机无 flock，跳过行为断言（静态门已全部执行）\n'
  exit 0
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

printf '\nOK: monitor_single_instance_gate_test 全部通过\n'
exit 0