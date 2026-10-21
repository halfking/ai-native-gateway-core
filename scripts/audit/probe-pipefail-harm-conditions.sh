#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# probe-pipefail-harm-conditions.sh —— 「危害成立的五个必要条件」的【形态探针】
#   （R99 轮 · 段十四 / §19.20.75）
#
# ★ 它回答什么：
#   「变量写端 → 管道 → 早退消费者」这一族，**在什么形态下**才会让 `set -o pipefail`
#   下的管道退出码变成 141（假非零）？—— 并且**逐条给出反例**（同样形态但无害的那一格）。
#
# ★★ **为什么单独一个探针（段十四新增）**：
#   段十三的探针只钉了「远离边界」的点（200 KB）⇒ 它能证明「危害成立」，但**证不了**
#   「危害不成立」的条件边界在哪儿。段十四补了两条**确定性**边界读数：
#     · grep-q：65,537 B ⇒ 0 ／ 70,000 B ⇒ 141（6/6 稳定 ⇒ 边界【确定】）
#     · head-c：60,000 B ⇒ 0 ／ 200,000 B ⇒ 141 ⇒ ★ **head 同样要求写端超 64 KiB**
#       ⇒ **订正段十三表格里「任意」的过宽表述**
#   ★★ **故意不钉 70,000 B 的 head 那一格**：实测它在边界上【有竞态】（连跑 8 次 ⇒
#     0 出现 3 次、141 出现 5 次）⇒ **把竞态读数钉进门 ⇒ 门必 flaky** ⇒ 只钉远离边界的点。
#
# ★★★ 本探针**自身含被测形态**（它必须真的跑管道才能测出 SIGPIPE）⇒
#   ⇒ 候选清单扫描面**必须显式排除本文件**（生成器里的 `EXCLUDE_MEASURE`）⇒
#   ⇒ 否则消费方会把它当成「待修的缺陷」。
#
# ★ 用法：`bash scripts/audit/probe-pipefail-harm-conditions.sh`
#   ⇒ 输出 TSV（6 列：写端|消费者|body形态|尺寸|命中位置|退出码）
#   ⇒ 第一行是 header（`header write_end consumer body_form size match_at exit_code`）
# ★ 退出码：本探针**永远返回 0**（它是量具，不是门；断言在 Go 门里）。
set -uo pipefail

OUT="${1:--}"
emit() { printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" "$6"; }

emit header write_end consumer body_form size match_at exit_code

# probe <写端> <消费者> <body形态> <字节数> <命中位置>
probe() {
  local we="$1" cons="$2" form="$3" size="$4" at_start="$5"
  local body="" rc=0
  if [ "$form" = long ]; then
    body="$(head -c "$size" /dev/zero | tr '\0' 'x')"        # ★ 单行（无换行）
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
    herestring)
      # ★★ 修法形态：写端【无管道】⇒ 写端不受 SIGPIPE ⇒ 退出码只反映消费者自己的判断
      grep -q MATCHME <<<"$body"
      rc=$? ;;
  esac
  emit "$we" "$cons" "$form" "$size" "$at_start" "$rc"
}

# ── 条件①：写端输出【单行】⇒ 无危害（grep 必须读到行尾才敢判 ⇒ 写端也写完了）────
probe printf grep-q long 200000 yes

# ── 条件②：写端【多行但未超管道缓冲 64 KiB】⇒ 无危害 ────────────────────────────
probe printf grep-q lines 1000 yes

# ── 条件②的【确定性边界】：grep-q 在 65,537 B 仍为 0、70,000 B 起为 141（6/6 稳定）──
probe printf grep-q lines 65537 yes
probe printf grep-q lines 70000 yes

# ── 条件①②③全中 ⇒ 危害成立（141 = 假非零 ⇒ 判定被翻转）─────────────────────────
probe printf grep-q lines 200000 yes

# ── 条件③：命中【靠后】⇒ 无害（grep 也要读到末尾 ⇒ 写端写完）────────────────────
probe printf grep-q lines 200000 no

# ── 条件⑤：head 消费者 ⇒ 同样要求写端超 64 KiB（60,000 B ⇒ 0）──────────────────
probe printf head-c lines 60000 yes
probe printf head-c lines 200000 yes

# ── ★★ 修法形态本身：herestring ⇒ 同一载荷下退出码 = 消费者自己的判断（0）────────
probe printf herestring lines 200000 yes
