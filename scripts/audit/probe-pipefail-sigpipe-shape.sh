#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# probe-pipefail-sigpipe-shape.sh —— 「SIGPIPE 竞态」的【形态判据】探针
#
# ★ 它回答一个问题（不是「某处是不是缺陷」，而是【判据本身成不成立】）：
#   「变量写端 → 管道 → 早退消费者」这一族，**在什么形态下**才会让
#    `set -o pipefail` 下的管道退出码变成 141（假非零）？
#
# ★ 实测结论（bash 5.3.9 / macOS arm64，2026-10-21；门 `scripts/ursmcheck/
#   section_1971_pipefail_sigpipe_shape_test.go` 会跑本探针并钉住这些读数）：
#
#   ┌────────────────┬──────────────────────┬──────────────┬───────────────┐
#   │ 写端输出形态   │ 消费者               │ 命中位置     │ 管道退出码    │
#   ├────────────────┼──────────────────────┼──────────────┼───────────────┤
#   │ 单行（无换行） │ grep -q              │ 任意         │ 0（稳定）     │
#   │ 多行 + ≥64 KiB │ grep -q              │ 靠前         │ 141（危害）   │
#   │ 多行 + <64 KiB │ grep -q              │ 靠前         │ 0（危害不成立）│
#   │ 任意           │ head -c N            │ 任意         │ 141（危害）   │
#   └────────────────┴──────────────────────┴──────────────┴───────────────┘
#
# ★★ 机制（可复算）：
#   ① `grep` 是【按行】匹配的 ⇒ 它必须读到一行的【行尾】才敢下结论；
#   ② 写端输出【只有一行】时 ⇒ grep 必须读完整条数据 ⇒ 写端也写完了
#      ⇒ 写端不会被 EPIPE/SIGPIPE 打断 ⇒ 退出码 = grep 自己的（0/1）⇒ **无危害**；
#   ③ 写端输出【多行】时 ⇒ grep 读完第 1 行就命中并【早退】
#      ⇒ 写端还堵在管道里（管道缓冲 64 KiB）⇒ 写端收到 EPIPE/SIGPIPE ⇒ **141**
#      ⇒ `pipefail` 取「最右非零」⇒ 管道退出码 = 141（**假非零**）
#      ⇒ 判定点 `if <管道>; then` **恒假** ⇒ 结论塌成「没匹配」。
#   ④ 在 `||` 链里，141（非零）会**触发下一个候选**⇒ 那条若也早退 ⇒ 141；
#      若不早退（读到末尾）⇒ 普通 1 ⇒ ⇒ 整条链仍是「非零」⇒ **结论照样塌**。
#
# ★★ ⇒ **判据（可操作形式）**：
#   「变量写端 → 管道 → 早退消费者」这一族，**只有当写端可能输出【多行】且
#    【无上界】时才是真缺陷面**；写端是【单条记录 / 单行标量】⇒ **无危害**
#    （改了也无收益）；消费者是 `head -c` ⇒ **无论行数都有害**。
#
# ★ 用法：`bash scripts/audit/probe-pipefail-sigpipe-shape.sh`
#   ⇒ 输出 TSV（形态、退出码），供门消费。
# ★ 退出码：本探针**永远返回 0**（它是量具，不是门；断言在 Go 门里）。
set -uo pipefail

PIPE_BUF="${PIPE_BUF:-65536}"      # 管道缓冲（Linux 默认 64 KiB；macOS 亦为 64 KiB 量级）
OUT="${1:--}"

emit() { printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" "$6"; }

emit header write_end consumer body_form size match_at exit_code

probe() {
  # $1=写端(内置)  $2=消费者(grep-q / head-c)  $3=body形态(long/lines)  $4=字节数  $5=命中位置(yes/no)
  # ★★ 这条注释**刻意不写被测形态**：写端与消费者若在这行里连成「变量 + 竖线 + 早退消费者」
  #   的字面形态，扫描器在【不剥注释】的口径下会把本探针自己算进候选清单 ⇒ 量具污染被测面。
  local we="$1" cons="$2" form="$3" size="$4" at_start="$5"
  local body="" rc=0
  if [ "$form" = long ]; then
    body="$(head -c "$size" /dev/zero | tr '\0' 'x')"          # ★ 单行（无换行）
  else
    body="$(python3 -c "import sys; sys.stdout.write(('x'*69+'\n')*max(1,$size//70))")"  # ★ 多行短行
  fi
  if [ "$at_start" = yes ]; then body="MATCHME${body}"; else body="${body}MATCHME"; fi

  case "$cons" in
    grep-q)
      if [ "$we" = echo ]; then echo "$body" | grep -q MATCHME; else printf '%s' "$body" | grep -q MATCHME; fi
      rc=$? ;;
    head-c)
      if [ "$we" = echo ]; then echo "$body" | head -c 10 >/dev/null; else printf '%s' "$body" | head -c 10 >/dev/null; fi
      rc=$? ;;
  esac
  emit "$we" "$cons" "$form" "$size" "$at_start" "$rc"
}

# ① 单行 + grep -q  ⇒ 期望 0（无危害）★ 这是「最容易被误判为有害」的一格
probe printf grep-q long  200000 yes
probe echo   grep-q long  200000 yes
# ② 多行 + < 缓冲 + grep -q ⇒ 期望 0（危害不成立：写端一次写完）
probe printf grep-q lines  1000   yes
# ③ 多行 + ≥ 缓冲 + grep -q ⇒ 期望 141（★ 危害成立）
probe printf grep-q lines  200000 yes
probe echo   grep-q lines  200000 yes
# ④ 命中位置在末尾 ⇒ grep 必须读到最后 ⇒ 写端写完 ⇒ 期望 0
probe printf grep-q lines  200000 no
# ⑤ head -c 消费者 ⇒ 无论行数都有害
probe printf head-c long  200000 yes
probe printf head-c lines 200000 yes
