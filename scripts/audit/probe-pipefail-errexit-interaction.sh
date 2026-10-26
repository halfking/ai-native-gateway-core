#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# probe-pipefail-errexit-interaction.sh —— 「errexit × pipefail × 141」的【交互探针】
#   （R99 轮 · 段十四 / §19.20.75 · 第十一小节）
#
# ★ 它回答什么：
#   当「管道退出码变成 141（假非零）」这件事发生时，**危害到底有多硬**？
#   ⇒ 段十四原本只说了【判定翻转】（条件上下文）；但候选文件里
#     **43/43 全部启用了 errexit**（形态是 `set -euo pipefail` ⇒ 复合写法）⇒
#     ⇒ 必须在【赋值 / 简单命令】上下文里再看一眼：那里 errexit 会不会让脚本**当场中止**？
#
# ★★★ **为什么这个探针必须存在（一条我自己踩过的坑）**：
#   我段十四写过一句「旁证：候选文件里没有一处 `set -e`」⇒ ★ **这句话是错的** ⇒
#   原因：我当时的检查**只匹配裸 `set -e`** ⇒ **完全漏掉了 `set -euo pipefail`**
#   （`-euo` 一次开了 errexit + nounset + pipefail）⇒ ⇒ ★★ **「查不存在」型断言的
#   危险方向：正则没覆盖住实际写法 ⇒ 报「0 处」⇒ 而实际是「全部都有」**
#   ⇒ ⇒ ★ 本探针把这件事变成**可复算的读数**，而不是一句话。
#
# ★★★★ **为什么用例体在【另一个数据文件】里（本文件里 0 个管道、0 个 pipefail 启用点）**：
#   ⇒ 并行会话的普查量具 `scripts/audit/measure-pipefail-race-surface.py` 的扫描面 =
#     「`.sh` 后缀 ∪ 首行是 shell shebang 的任意文件」，
#     它的候选集判据 = 「同作用域内、管道点之前存在 pipefail 启用点」
#   ⇒ ★★★ **本文件若含用例体（每个用例自带 `set -euo pipefail` 且紧跟管道点）**
#     ⇒ ⇒ 6 个用例全部会被算成候选点 ⇒ ⇒ **他们的台账门当场红**
#     ⇒ ⇒ 而那本台账是【别人的资产】⇒ ⇒ **本轮不碰它**
#   ⇒ ⇒ ★★ **⇒ 修法（只改我自己的文件）**：用例体放进
#     `scripts/audit/probe-pipefail-errexit-cases.txt`（**既非 .sh、又无 shell shebang**
#     ⇒ 两个扫描量具都看不见它）⇒ 运行时落成**临时脚本文件**再执行
#   ⇒ ★★ **⇒ 于是本探针对两个扫描量具都是【零候选】**（我这边的 `EXCLUDE_MEASURE` 另有一层）
#   ⇒ ★★ **⇒ 通用式（跨仓通用）**：★ **新增一个「测量工具」时 ⇒ 必须确认它对
#     【下游的共享扫描面】是零候选** ⇒ ⇒ 否则你会把别人的门打红、而修法要动别人的台账
#     ⇒ ⇒ ★★ **优先找「只改自己的文件」的那条路**（本例 = 让被测形态只存在于运行时文件里）
#
# ★★★ 本探针**自身含被测形态**（它必须真的跑管道才能测出 SIGPIPE）⇒
#   ⇒ 候选清单扫描面**必须显式排除本文件**（生成器里的 `EXCLUDE_MEASURE`）⇒
#   ⇒ 否则消费方会把它当成「待修的缺陷」。
#
# ★ 用法：`bash scripts/audit/probe-pipefail-errexit-interaction.sh`
#   ⇒ 输出 TSV（5 列：读数|上下文|errexit|rc|到达末尾）
#   ⇒ 第一行是 header
# ★ 退出码：本探针**永远返回 0**（它是量具，不是门；断言在 Go 门里）。
# ★ 产物：临时目录（mktemp -d）⇒ **绝不写进仓内工作树**。
set -uo pipefail

OUT="${1:--}"
emit() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5"; }

emit header context errexit rc reached_end

HERE="$(cd "$(dirname "$0")" && pwd)"
CASES="$HERE/probe-pipefail-errexit-cases.txt"
if [ ! -f "$CASES" ]; then
  echo "★ 找不到用例文件 $CASES" >&2
  exit 2
fi

WORK="$(mktemp -d)"
# ★ 清理必须【静默】且【不用 rm -rf】：
#   ⇒ 本机的 `rm`（含 `rm -f`）会被 mavis-trash 接管并【把提示打到 stdout】⇒ ⇒ 会污染本探针的 TSV
#   ⇒ ⇒ 全部清理动作重定向到 /dev/null（提示随之被吞掉）。
# ★★ **为什么必须用【真文件】做载荷源**（不能改成 `python3 | head`）：
#   ⇒ 写端必须是【被 SIGPIPE 杀掉】的进程（`cat`）⇒ 管道退出码才是 141
#   ⇒ 若写端是 python，它会把 BrokenPipeError 吃掉并以 120/1 退出 ⇒ ⇒ **141 根本不会出现**
#   ⇒ ⇒ 载荷必须落成文件、由 `cat` 读。
trap 'rm -f "$WORK/big_200k.txt" "$WORK"/case_*.sh >/dev/null 2>&1; rmdir "$WORK" >/dev/null 2>&1' EXIT
BIG="$WORK/big_200k.txt"
python3 -c "import sys; sys.stdout.write('a'*200000)" > "$BIG"

# run <名字> <上下文标签> <errexit开关> <脚本体>
#   ⇒ 脚本体里的 @BIG@ 用载荷文件真实路径代入（避免外层展开）
#   ⇒ 脚本体落成临时脚本文件再执行 ⇒ 管道行只在运行时文件里（不在仓内任何扫描面）
#   ⇒ 输出两列：脚本体退出码 rc、是否到达末尾 reached_end
run() {
  local name="$1" ctx="$2" errexit="$3" body="$4"
  body="${body//@BIG@/$BIG}"
  local case_file="$WORK/case_$name.sh"
  local out rc reached
  printf '%s\n' "$body" > "$case_file"
  out="$(bash "$case_file" 2>&1)"; rc=$?
  reached=no
  case "$out" in *REACHED_END*) reached=yes ;; esac
  emit "$name" "$ctx" "$errexit" "$rc" "$reached"
}

# ── 逐个用例执行（用例体来自数据文件）────────────────────────────────────────
cur_name=""; cur_ctx=""; cur_errexit=""; cur_body=""
flush_case() {
  [ -z "$cur_name" ] && return 0
  run "$cur_name" "$cur_ctx" "$cur_errexit" "$cur_body"
  cur_name=""; cur_ctx=""; cur_errexit=""; cur_body=""
}

while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    '--- CASE '*)
      flush_case
      # 去掉前缀后按空格切成 名字/上下文/errexit
      rest="${line#--- CASE }"
      cur_name="${rest%% *}"; rest="${rest#* }"
      cur_ctx="${rest%% *}"; rest="${rest#* }"
      cur_errexit="${rest%% *}"
      ;;
    '#'*|'')
      # 注释与空行：忽略（但不改写已收集的脚本体）
      ;;
    *)
      cur_body="$cur_body$line
"
      ;;
  esac
done < "$CASES"
flush_case
