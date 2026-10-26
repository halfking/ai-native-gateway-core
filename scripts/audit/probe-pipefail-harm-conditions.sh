#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# probe-pipefail-harm-conditions.sh —— 「危害成立的必要条件」的【形态探针】
#   （R99 轮 · 段十四 / §19.20.75；★ R85 轮 · §19.25.17 修订【读数形态】与【判定式】）
#
# ★ 它回答什么：
#   「变量写端 → 管道 → 早退消费者」这一族，**在什么形态下**才会让 `set -o pipefail`
#   下的管道退出码变成 141（假非零）？—— 并且**逐条给出反例**（同样形态但无害的那一格）。
#
# ★★★★ 【R85 轮 · 订正段十四的两处表述】（实测依据见 handoff §19.25.17）
#
#   ① ★★★ 「危害成立」的**真正必要条件不是「写端超 64 KiB」** ⇒ ★ 它是
#      **「消费者在写端写完之前退出」**。而「超 64 KiB」只是
#      **「写端必然写不完」的【充分代理】**（不是必要条件）⇒ ★ 两者在
#      **载荷 ≈ 管道缓冲**这一段**不等价** ⇒ ⇒ ⇒ ★★ 【64 KiB 不是一个【阈值】，
#      而是一个【概率穿越点】】★ ★ ⇒ 详见下方「三条机制的统一表述」。
#
#   ② ★★★ **「6/6 稳定 ⇒ 边界确定」这个依据被实测证伪** ⇒ ★ 载荷落在管道缓冲
#      附近的那几格是**真竞态** ⇒ ★ **单次读数只是一次观测，不是一个性质**
#      ⇒ ⇒ ⇒ **本探针改为【每条读数采样 N 次】⇒ 同时输出【众数】与【取值集合】**
#      ⇒ ⇒ ⇒ **Go 门据此把读数分成三类**（稳定-0 / 稳定-141 / 竞态）⇒
#      ⇒ ⇒ ★ **竞态类不再被钉成定值**（那正是 R84 轮实测出的 flaky 源）。
#
#   ③ ★★ **TSV 表头与数据列曾经错位一列**（`emit` 6 个 `%s`、表头 7 个词）
#      ⇒ ★ 对外产物按表头取列会取错列 ⇒ ⇒ **本版把表头与数据对齐成同宽**。
#
# ★★★★ 【三条机制的统一表述】（R85 轮实测，宿主 = macOS/darwin-arm64，
#   实测管道缓冲 = 65536 字节，5/5 次一致）：
#
#   ★ 管道退出码变成 141 的**充要条件** = **消费者先退出 ⇒ 写端尚未写完 ⇒ 写端被 SIGPIPE**。
#   ⇒ ★ 三个因子各管一段：
#     ① 【消费者何时退出】
#        · `grep -q` + **单行**（无换行）⇒ ★ **必须读到 EOF 才敢判** ⇒ 读端不早退
#        · `grep -q` + **多行** + **命中靠前** ⇒ 读到首块即可判 ⇒ ★ **早退**
#        · `grep -q` + **命中靠后** ⇒ 必须读到末尾 ⇒ 不早退
#        · `head -c 10` ⇒ 读到 10 字节即退 ⇒ ★ **早退**
#     ② 【写端能否写完】
#        · 载荷 ≫ 管道缓冲 ⇒ **必然写不完** ⇒ ⇒ 危害**确定**成立
#        · 载荷 ≪ 管道缓冲 ⇒ **多半已写完** ⇒ ⇒ 危害**多半**不成立
#        · 载荷 ≈ 管道缓冲 ⇒ ★★ **赛跑** ⇒ ⇒ 两种结果**都可能出现**
#     ③ 【修法形态】herestring ⇒ 写端【不在管道里】⇒ **不可能**被 SIGPIPE ⇒ 恒 0
#
#   ⇒ ⇒ ⇒ ★★ **因此「五条件」里���条件②的正确表述是**：
#      **「写端【确定】写不完」**（其静态可判代理 = 「载荷超管道缓冲」）。
#      ⇒ ★ 作为【静态筛查器】条件②依然成立（它【充分】）⇒ ★ 只需把**表述**改精确。
#
# ★ 用法：`bash scripts/audit/probe-pipefail-harm-conditions.sh`
#   ⇒ 输出 TSV（7 列：write_end|consumer|body_form|size|match_at|exit_code|observed）
#   ⇒ 第 1 行是**表头**（7 个列名，与数据行同宽 ⇒ **逐列对齐**）
#   ⇒ `exit_code` = N 次采样的**众数**；`observed` = N 次采样**去重排序后**的取值集合
#     （形如 `0` 或 `0,141`）⇒ ★ ★ **竞态格会在这里显形** ⇒ 门据此分类。
#   ⇒ 采样次数：环境变量 `SC1975_SAMPLES`（默认 5；必须是 ≥1 的整数）。
# ★ 退出码：本探针**永远返回 0**（它是量具，不是门；断言在 Go 门里）。
set -uo pipefail

OUT="${1:--}"

# ★★ 采样次数：每条读数连跑 N 次 ⇒ 输出【众数】+【取值集合】。
#   ★ 为什么默认 5：★ 奇数 ⇒ 众数无歧义；★ 5 次足以让「稳定格」在一次探针运行里
#   就被**多次独立采样**确认（而不是靠「单次读数恰好是 0」）。
SAMPLES="${SC1975_SAMPLES:-5}"
case "$SAMPLES" in
  ''|*[!0-9]*) SAMPLES=5 ;;
esac
[ "$SAMPLES" -ge 1 ] 2>/dev/null || SAMPLES=5

# ★★ emit 的**列数 = 表头列数 = 数据列数**（R85 轮订正段十四的错位一列）。
emit() { printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" "$6" "$7"; }

emit write_end consumer body_form size match_at exit_code observed

# run_once <写端> <消费者> <body> <命中位置> ⇒ 把该次采样的**退出码**打到 stdout。
# ★ 这里只取【值】不用【命令替换自身的退出码】（命令替换的退出码 = run_onec 自己的）。
run_once() {
  local we="$1" cons="$2" body="$3" at_start="$4" rc=0
  case "$cons" in
    grep-q)
      if [ "$we" = echo ]; then echo "$body" | grep -q MATCHME; else printf '%s' "$body" | grep -q MATCHME; fi
      rc=$? ;;
    head-c)
      if [ "$we" = echo ]; then echo "$body" | head -c 10 >/dev/null; else printf '%s' "$body" | head -c 10 >/dev/null; fi
      rc=$? ;;
    herestring)
      # ★★ 修法形态：写端【无管道】⇒ 写端不受 SIGPIPE ⇒ 退出码只反映消费者自己的判断
      grep -q MATCHME <<<"$body"
      rc=$? ;;
  esac
  printf '%s' "$rc"
}

# probe <写端> <消费者> <body形态> <字节数> <命中位置>
probe() {
  local we="$1" cons="$2" form="$3" size="$4" at_start="$5"
  local body="" i=0 mode="" observed=""
  if [ "$form" = long ]; then
    body="$(head -c "$size" /dev/zero | tr '\0' 'x')"        # ★ 单行（无换行）
  else
    body="$(python3 -c "import sys; sys.stdout.write(('x'*69+'\n')*max(1,$size//70))")"  # ★ 多行短行
  fi
  if [ "$at_start" = yes ]; then body="MATCHME${body}"; else body="${body}MATCHME"; fi

  # ★★ 采样 N 次 ⇒ 收【取值集合】与【众数】。
  local vals=""
  for i in $(seq 1 "$SAMPLES"); do
    vals="${vals}$(run_once "$we" "$cons" "$body" "$at_start")
"
  done
  observed=$(printf '%s' "$vals" | sort -u | paste -sd, -)
  mode=$(printf '%s' "$vals" | sort | uniq -c | sort -rn | head -1 | awk '{print $2}')

  emit "$we" "$cons" "$form" "$size" "$at_start" "$mode" "$observed"
}

# ── 条件①：写端输出【单行】⇒ 无危害（grep 必须读到行尾才敢判 ⇒ 写端也写完了）────
probe printf grep-q long 200000 yes

# ── 条件②：写端【多行但未超管道缓冲 64 KiB】⇒ 无危害 ────────────────────────────
probe printf grep-q lines 1000 yes

# ── ★★ 【竞态区】载荷 ≈ 管道缓冲 ⇒ ★ 两种结果都可能 ⇒ **不钉定值** ───────────────
probe printf grep-q lines 65537 yes
probe printf grep-q lines 70000 yes

# ── 条件①②③全中 ⇒ 危害成立（141 = 假非零 ⇒ 判定被翻转）─────────────────────────
probe printf grep-q lines 200000 yes

# ── 条件③：命中【靠后】⇒ 无害（grep 也要读到末尾 ⇒ 写端写完）────────────────────
probe printf grep-q lines 200000 no

# ── 条件⑤：head 消费者 ⇒ 同样要求写端超 64 KiB（60,000 B ⇒ 【竞态区】）──────────
probe printf head-c lines 60000 yes
probe printf head-c lines 200000 yes

# ── ★★ 修法形态本身：herestring ⇒ 同一载荷下退出码 = 消费者自己的判断（0）────────
probe printf herestring lines 200000 yes
