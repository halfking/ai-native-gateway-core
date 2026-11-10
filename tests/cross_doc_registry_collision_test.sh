#!/usr/bin/env bash
# =====================================================================
# tests/cross_doc_registry_collision_test.sh
#
# ★ 跨档登记项【同号异义】检测门（★ 第五十二轮 · F44 收口）
#
# ★ 这道门为什么存在
#
# 仓里有**两份**审计档，各带一份登记册：
#   · docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md（主档，
#     §28.23 是登记项现状的**唯一规范源**）
#   · docs/12小时内修订审计-*.md（兄弟档，窗口档）
# ⇒ 两份档**各自**都有编号纪律、**各自**都有机器门 ⇒ 但
#   **没有任何一道门跨档检查「同一个号在两档指的是不是同一件事」**
# ⇒ ⇒ 而**同号异义**的后果是：任何跨档引用「F-7」都有歧义 ⇒
#   而本仓的纪律是「**登记必有号**」⇒ ⇒ **编号这个「追溯锚点」被自身破坏**。
#
# ★ ★★ 既有门为什么看不见它（★ 这是本门存在的直接理由）
#
# ① scripts/ursmcheck/audit_cross_doc_state_sync_test.go 的 `xdSibDocRelPath`
#    **写死**了 `docs/12小时内修订审计-2026-11-02-0350.md` ⇒
#    而那已经是**上一份**窗口档 ⇒ ⇒ **当前活跃窗口档的登记项一条都没进门**。
# ② tests/audit_ledger_consistency_test.sh 的 `SIB_DOC` **同样写死**那一份。
# ③ 兄弟档的 §三 表**没有状态列**（编号/严重度/位置/问题/证据）⇒
#    而状态是**唯一**能机器判的「两档说的是不是同一件事」的代理信号。
#    ⇒ ★ 兄弟档的状态在**另一张表**里（§4.2 执行规划表，列：序/编号/动作/
#      **本轮是否落地**/理由）⇒ ⇒ 既有抽取器**看不见它**。
#
# ★ ★★ 本门的判据（★ 三个可复现的读数）
#
# 判据 X11 = **状态分歧**：
#   对同一个编号，两档的**状态类**必须相同（closed / intentional / open）。
#   ⇒ 同号异义 ⇒ 讲的是两件事 ⇒ 状态极可能不同 ⇒ 分歧即候选命中。
#
# ★ ★★ 为什么这条判据**不会假阳性**（★ 实测，不是推理）
#
#   · 在**同一件事**的窗口档上（`2026-11-02-0350`，它的 F-N 与主档 F-N
#     同义）：**32 行可比、32 行一致、0 条分歧** ⇒ FP = 0。
#   · 在**当前活跃**窗口档上（`20261104-0329`，它讲的是另一批事）：
#     **命中 7 条**（F7 / F10 / F11 / F13 / F14 / F17 / F21）
#     ⇒ 而这 7 条**逐条核对过**：每一对两侧讲的确实是两件不同的事
#       （例：主档 F7 ＝ Anthropic 腿 reasoning trace、兄弟档 F-7 ＝
#         预热 HSET+EXPIRE 合事务 ⇒ 两件事 ⇒ 状态一 open 一 closed ⇒ 命中）。
#
# ★ ★★ 「活跃窗口档」怎么定（★ 不写死文件名）
#
#   按**提交时间**在仓里挑**最新的、且带状态列的**窗口档
#   （⇒ 兄弟档 §4.2 那种「序/编号/动作/本轮是否落地/理由」的表）。
#   ⇒ ★ **不写死任何文件名** ⇒ ★ 新窗口档落地后这道门自动跟上。
#   ⇒ 而 `AUDIT_SIB_DOC` 可显式覆盖（★ 供夹具与自测用）。
#
# ★ ★★ 兄弟档已经自己采纳的收敛约定（★ 本门尊重它）
#
#   兄弟档 §8.8 / §8.15 定的约定是「★ **直接用主档号** ＋ 窗口档只留
#   `主档 F-XX` 指针」⇒ ⇒ 而**带指针**的行（★ 例：`**F-20**（★ **主档号
#   `F54`** ★））**已经消歧** ⇒ ⇒ 本门对**带指针**的行**不判分歧**
#   （★ 指针已经说清它指的是主档哪一条）⇒ ⇒ 只对**裸号**行判。
#
# ★ 退出码：0 全绿；1 有分歧（★ 红门 ⇒ 逼出真分叉）。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

MAIN_DOC="docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

# ── 状态类判定（★ 与主档既有词表同源）────────────────────────────
# ★ closed / intentional / open / '?'（★ 词表外的写法 ⇒ 不判）
state_class() {
  local s="$1"
  case "$s" in
    *已闭合*|*已关闭*|*已实施*|*已落地*|*本轮落地*|*已由并行会话落地*) echo closed ;;
    *有意如此*) echo intentional ;;
    *仍开*|*留待下一轮*|*归用户裁决*) echo open ;;
    *) echo '?' ;;
  esac
}

# ── 主档规范源：编号 → 状态类 ───────────────────────────────────
main_registry() {
  awk -F'|' '
    # ★ 锚点必须是 **§28.23 的标题行本身**（★ 不能只匹配「现状总表」这个词
    #   —— ★ §28.22 正文里也出现过它 ⇒ 那样会锚到一张无关的表）。
    /^### 28\.23 / && !seen { seen=1; next }
    seen && /^#{2,3} / { exit }
    seen && /^\| \*\*F/ {
      id=$2; st=$3
      gsub(/^[ \t]*\*\*F/, "", id); gsub(/\*\*[ \t]*$/, "", id)
      gsub(/^[ \t]+|[ \t]+$/, "", id)
      gsub(/^[ \t]+|[ \t]+$/, "", st)
      printf "%s\t%s\n", id, st
    }
  ' "$MAIN_DOC"
}

# ── 兄弟档：裸号行 → 编号 + 状态类 ──────────────────────────────
# ★ 兄弟档有两种登记表头（★ 两种都要认 ⇒ 否则判据面会静默缩水）：
#   · 形状 A（★ §三 问题清单表）：`| **F1** | 级别 | 位置 | 问题 | 状态 |`
#     ⇒ 编号在 cells[1]、状态在 cells[5]
#   · 形状 B（★ §4.2 执行规划表）：`| 序 | **F-1** | 动作 | **本轮是否落地** | 理由 |`
#     ⇒ 编号在 cells[2]、状态在 cells[4]
# ★ **带 `主档号` 指针的行**（★ 例：**F-20**（★ **主档号 `F54`** ★））
#   **已经消歧** ⇒ ⇒ 不判（★ 指针已经说清它指的是主档哪一条）。
# ★ 状态词表外的格（★ 如「证据」列里偶然出现的词）⇒ 判为 '?' ⇒ 不判。
#
# ★ mutate=<行号> 时：★ 不打印、★ 而是把那一行的状态格改写成「仍开」
#   ⇒ 供变异臂用（★ 证明本门看得见分歧、不是一个恒绿的门）。
sibling_bare_rows() {
  local doc="$1" mutate="${2:-}"
  awk -F'|' -v mutate="$mutate" '
    function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
    function is_status_cell(s) {
      return (s ~ /已闭合|已关闭|已实施|已落地|本轮落地|已由并行会话落地|有意如此|仍开|留待下一轮|归用户裁决/)
    }
    {
      nc = NF
      idc = ""; stc = ""
      # ★ awk 的字段从 1 起、★ 而 `| a | b |` 切出来的第一个字段是**空串**
      #   ⇒ ★ 于是「编号在 cells[1]」在 awk 里就是 **$2**、★
      #   「状态在 cells[5]」就是 **$6**（★ §三 形状）。
      #   §4.2 形状（`| 序 | **F-1** | 动作 | 状态 | 理由 |`）⇒ 编号 **$3**、状态 **$5**。
      if (nc >= 7 && trim($3) ~ /^\*\*F-[0-9]+\*\*$/) {
        # ★ 形状 B（§4.2 执行规划表）：编号格 $3、状态格 $5
        idc = trim($3); stc = trim($5)
      } else if (nc >= 7 && trim($2) ~ /^\*\*F[0-9]+\*\*$/) {
        # ★ 形状 A（§三 问题清单表）：编号格 $2、状态格 $6
        idc = trim($2); stc = trim($6)
      } else { next }
      # ★ 带「主档号」指针 ⇒ 已消歧 ⇒ 跳过（★ 整行扫 ⇒ 指针在哪个格都认）
      if ($0 ~ /主档号|主档 F[0-9]/) next
      # ★ 编号格必须真的是编号（★ 排除 3.3 / CI 秘密扫描门 这类非编号行）
      # ★ ★ 编号格可以有**多个**编号（★ 例：`**F-8 / F-4**`）
      #   或带**后缀**（★ 例：`**F-2 延伸**`）⇒ ★ 都要逐个取出来。
      if (idc !~ /\*\*F-?[0-9]+/) next
      if (stc !~ /已闭合|已关闭|已实施|已落地|本轮落地|已由并行会话落地|有意如此|仍开|留待下一轮|归用户裁决/) next
      # ★ 逐个编号出列（★ `**F-8 / F-4**` ⇒ 两个；★ `**F-2 延伸**` ⇒ 一个）
      rest = idc
      while (match(rest, /\*\*F-?[0-9]+/)) {
        tok = substr(rest, RSTART, RLENGTH)
        n = tok; gsub(/\*\*F-?/, "", n)
        if (n ~ /^[0-9]+$/) {
          if (mutate != "" && NR == mutate) { printf "%s\t%s\t%d\tMUTATED\n", n, "仍开", NR }
          else { printf "%s\t%s\t%d\n", n, stc, NR }
        }
        rest = substr(rest, RSTART + RLENGTH)
      }
    }
  ' "$doc"
}

# ── 活跃窗口档：最新的、带状态列的窗口档 ────────────────────────
# ★ 不写死文件名 ⇒ 新窗口档落地后自动跟上。
resolve_active_sib() {
  if [[ -n "${AUDIT_SIB_DOC:-}" ]]; then
    printf '%s\n' "$AUDIT_SIB_DOC"
    return 0
  fi
  local best="" best_ts=0 cand ts
  while IFS= read -r cand; do
    [[ -f "$cand" ]] || continue
    # ★ 必须是「带状态列」的窗口档 ⇒ 用「序/编号/动作/本轮是否落地」表头判定
    grep -qE '^\|[[:space:]]*序[[:space:]]*\|[[:space:]]*编号[[:space:]]*\|[[:space:]]*动作' "$cand" || continue
    ts=$(git log -1 --format=%ct -- "$cand" 2>/dev/null || echo 0)
    if [[ "$ts" -gt "$best_ts" ]]; then best_ts=$ts; best="$cand"; fi
  done < <(git -c core.quotepath=false ls-tree -r HEAD --name-only \
           | grep -E '^docs/12小时内修订审计.*\.md$' || true)
  [[ -n "$best" ]] && printf '%s\n' "$best"
}

SIB_DOC="$(resolve_active_sib)"

if [[ -z "$SIB_DOC" || ! -f "$SIB_DOC" ]]; then
  bad "解析不到活跃窗口档（★ 既没找到带状态列的窗口档、文件也不存在）"
  echo "PASS=$PASS FAIL=$FAIL"
  exit 1
fi

echo "ℹ️ 活跃窗口档：$SIB_DOC"
echo "ℹ️ 主档规范源：$MAIN_DOC"
echo

# ── 判据 X11：同号 ⇒ 状态类必须一致 ──────────────────────────────
MAIN_REG="$(main_registry)"
[ -n "$MAIN_REG" ] || { bad "主档规范源抽取为空"; echo "PASS=$PASS FAIL=$FAIL"; exit 1; }

declare -a HITS=()
comparable=0
while IFS=$'\t' read -r nid nst nln; do
  [[ -n "$nid" ]] || continue
  mst=$(printf '%s\n' "$MAIN_REG" | awk -F'\t' -v k="$nid" '$1==k{print $2; exit}')
  [[ -n "$mst" ]] || continue          # ★ 主档没这个号 ⇒ 不在射程内
  mcls=$(state_class "$mst")
  scls=$(state_class "$nst")
  [[ "$scls" == '?' ]] && continue      # ★ 词表外 ⇒ 不判
  comparable=$((comparable + 1))
  if [[ "$mcls" != "$scls" ]]; then
    HITS+=("$nid|主档=$mcls($mst)|兄弟档=$scls($nst)|兄弟档行=$nln")
  fi
done < <(sibling_bare_rows "$SIB_DOC")

echo "ℹ️ 可比编号（同号且两档都能判状态）：$comparable"
echo "ℹ️ 状态分歧命中：${#HITS[@]}"
if [[ ${#HITS[@]} -gt 0 ]]; then
  for h in "${HITS[@]}"; do
    IFS='|' read -r a b c d <<<"$h"
    printf '   ⚠️  F%s  %s  %s  %s\n' "$a" "$b" "$c" "$d"
  done
fi
echo

# ── 断言 ③：★ 棘轮：★ **新**分歧必须红（★ 而基线里的已知分歧不红）──
# ★ ★ 兄弟档 §8.8 / §8.15 自己采纳的方案是「★ 先跑只读告警模式、★
#   把「该改多少」变成实测数、★ 编号收敛后再转成红门」⇒
# ⇒ 而**编号收敛要动兄弟档的编号**（★ 兄弟会话的文件、★ 本仓不代改）⇒
# ⇒ ⇒ 所以本门用**基线棘轮**落地（★ 棘轮面 = tests/cross_doc_registry_collision_baseline.tsv）：
#   · 基线里的分歧 ＝ 已知、已登记、待兄弟档收敛 ⇒ 不红
#   · **新**分歧（不在基线里）⇒ ★ **红** ← ★ 这才是「逼出真分歧」的那一步
#   · 基线里**已不再分歧**的条目（★ 收敛了、而基线没更新）⇒ ★ **红**
#     （★ 棘轮双向：★ 收敛必须体现在基线缩小上、⇒ 不许悄悄遗忘）
BASELINE="tests/cross_doc_registry_collision_baseline.tsv"
if [[ ! -f "$BASELINE" ]]; then
  bad "基线文件缺失：$BASELINE（★ 棘轮面必须存在）"
else
  # 基线条目：编号 \t 行号（★ 行号只用于核对、★ 判定按编号）
  # ★ 编号归一：★ 基线里写 `F10`、门判出来的是 `10` ⇒ ★ 两侧都去掉 `F` 前缀再比。
  base_ids="$(awk -F'\t' '!/^#/ && NF>=2 && $1!="" {gsub(/^[Ff]/, "", $1); print $1}' "$BASELINE" | sort -u)"
  base_count=$(printf '%s\n' "$base_ids" | grep -c . || true)
  new_hits=0
  for h in "${HITS[@]}"; do
    IFS='|' read -r a b c d <<<"$h"
    if ! printf '%s\n' "$base_ids" | grep -qx "$a"; then
      new_hits=$((new_hits + 1))
      printf '   ❌ 新分歧 F%s（★ 不在基线里 ⇒ ★ 必须红）\n' "$a" >&2
    fi
  done
  # ★ 基线里已不再分歧的条目（★ 收敛了、而基线没缩小）
  stale=0
  hit_ids="$(for h in "${HITS[@]}"; do IFS='|' read -r a _ _ _ <<<"$h"; echo "$a"; done | sort -u)"
  while IFS= read -r bid; do
    [[ -n "$bid" ]] || continue
    if ! printf '%s\n' "$hit_ids" | grep -qx "$bid"; then
      stale=$((stale + 1))
      printf '   ❌ 基线里的 F%s 已不再分歧（★ 兄弟档收敛了 ⇒ ★ 基线必须删掉它）\n' "$bid" >&2
    fi
  done <<<"$base_ids"
  if [[ "$new_hits" -eq 0 ]]; then
    ok "棘轮：★ 无新分歧（★ 命中 ${#HITS[@]} 条全部在基线内 · 基线 $base_count 条）"
  else
    bad "棘轮：★ 出现 $new_hits 条新分歧（★ 不在基线里 ⇒ 同号异义在扩大）"
  fi
  if [[ "$stale" -eq 0 ]]; then
    ok "棘轮反向：★ 基线里没有已收敛却未删的条目"
  else
    bad "棘轮反向：★ 基线里 $stale 条已不再分歧（★ 收敛没体现在基线上）"
  fi
fi
echo

# ── 断言 ①：抽取器自己有牙（★ 规范源缺失 ⇒ 红）─────────────────
if [[ -z "$MAIN_REG" ]]; then
  bad "抽取器没有牙：★ 主档规范源抽取为空（★ 本门应当红）"
else
  ok "抽取器有牙：★ 主档规范源抽取到 $(printf '%s\n' "$MAIN_REG" | wc -l | tr -d ' ') 条"
fi

# ── 断言 ②：活跃窗口档可解析且带状态列 ─────────────────────────
if [[ -n "$(sibling_bare_rows "$SIB_DOC")" ]]; then
  ok "活跃窗口档的裸号行可抽取（★ 状态列读得到）"
else
  bad "活跃窗口档的裸号行抽不到（★ 状态列没读出来 ⇒ 本门会绿着漏）"
fi

# ── 断言 ③：★ 同号异义必须被逼出来（★ 判据面棘轮）────────────
# ★ 活跃窗口档里确实存在同号异义（★ 兄弟档 §8.8 自己实测过）⇒
# ⇒ 本门在**它**上面必须命中 ⇒ ★ 命中数下限 = 1（★ 归零 ⇒ 判据失效）。
if [[ ${#HITS[@]} -ge 1 ]]; then
  ok "同号异义被逼出来：★ 命中 ${#HITS[@]} 条（★ ≥1 ⇒ 判据有牙）"
else
  bad "同号异义一条都没逼出来（★ 判据面失效或活跃窗口档已换）"
fi

# ── 断言 ④：★ 假阳性必须为 0（★ 在「同一件事」的窗口档上）─────
# ★ 用兄弟档里**与主档同义**的那份窗口档做对照 ⇒ 那里不该有分歧。
# ★ 该档由 AUDIT_SIB_CONTROL 覆盖；缺省用仓里同名的「2026-11-02-0350」。
# ★ ★ 判据面必须**非零**（★ 零行 ⇒ 这个断言就是空的 ⇒ 绿着漏）。
FP_DOC="${AUDIT_SIB_CONTROL:-$REPO_ROOT/docs/12小时内修订审计-2026-11-02-0350.md}"
if [[ ! -f "$FP_DOC" ]]; then
  bad "对照档缺失：${FP_DOC}（★ 无法测假阳性）"
else
  fp=0; fp_cmp=0; fp_excluded=0
  FP_REG="$MAIN_REG"
  while IFS=$'\t' read -r nid nst nln; do
    [[ -n "$nid" ]] || continue
    mst=$(printf '%s\n' "$FP_REG" | awk -F'\t' -v k="$nid" '$1==k{print $2; exit}')
    [[ -n "$mst" ]] || continue
    mcls=$(state_class "$mst")
    scls=$(state_class "$nst")
    [[ "$scls" == '?' ]] && continue
    # ★ ★★ 排除「★ 主档已闭合 ★」的那些行（★ 2026-11-10 实测踩到 ★）：
    #   ★ 对照档是**旧快照**（★ 它记的是主档**当时**的状态 ★）⇒ ★ 主档把某一项
    #   ★ 闭合之后、★ 对照档那一行还写着「仍开」⇒ ⇒ 判据会判它「分歧」⇒
    #   ★ ★ **而那不是「同号异义」、★ 是「状态漂移」★★ ⇒ ⇒ 把它排除、
    #   ★ 且**把排除条数打印出来**（★ 不许悄悄排除 ★）
    if [[ "$mcls" == "closed" ]]; then
      fp_excluded=$((fp_excluded + 1))
      continue
    fi
    fp_cmp=$((fp_cmp + 1))
    [[ "$mcls" != "$scls" ]] && fp=$((fp + 1))
  done < <(sibling_bare_rows "$FP_DOC")
  if [[ "$fp_excluded" -gt 0 ]]; then
    echo "ℹ️ 假阳性对照：★ 排除 $fp_excluded 行（★ 主档已闭合、★ 而对照档是旧快照 ⇒ 那是状态漂移、★ 不是同号异义 ★）"
  fi
  if [[ "$fp_cmp" -eq 0 ]]; then
    bad "假阳性对照档（${FP_DOC##*/}）可比行数 = 0（★ 判据面缩水 ⇒ 这个断言是空的 ⇒ 绿着漏）"
  elif [[ "$fp" -eq 0 ]]; then
    ok "假阳性 = 0（★ 同义窗口档 ${FP_DOC##*/} 上 $fp_cmp 行可比、零分歧）"
  else
    bad "假阳性 = ${fp}（★ 同义窗口档 ${FP_DOC##*/} 上 $fp_cmp 行可比却出现分歧 ⇒ 判据不成立）"
  fi
fi

# ── 断言 ⑤：★ 带 `主档号` 指针的行**不判**（★ 已消歧）──────────
PTR_DOC="$SIB_DOC"
ptr_rows=$(awk -F'|' '
  /^\|[[:space:]]*[0-9]+[[:space:]]*\|/ {
    idc=$3; gsub(/^[ \t]+|[ \t]+$/, "", idc)
    if (idc ~ /主档号|主档 F/) { c++; if (idc ~ /\*\*F-[0-9]+\*\*/) n++ }
  }
  END { printf "%d\t%d\n", c+0, n+0 }
' "$PTR_DOC")
ptr_all="${ptr_rows%%$'\t'*}"
ptr_num="${ptr_rows##*$'\t'}"
if [[ "$ptr_all" -gt 0 ]]; then
  ok "带指针的行被排除在判据面外（★ 指针行 $ptr_all 条、其中带编号 $ptr_num 条 ⇒ 已消歧）"
else
  ok "本窗口档没有带 `主档号` 指针的行（★ 判据面覆盖全部裸号行）"
fi

# ── 断言 ⑥：★ 量具自己有牙（★ 变异臂）─────────────────────────
# ★ 在**对照档**（★ 同义、当前零分歧）上把一行状态改掉
#   ⇒ 分歧数必须从 0 变成 ≥1 ⇒ ⇒ 证明本门**看得见**分歧
#   ⇒ 而不是一个恒绿的门。
mut_dir="$(mktemp -d)"
trap 'rm -rf "$mut_dir"' EXIT
if [[ -f "$FP_DOC" ]]; then
  # ★ 挑对照档里**第一行可比行**（★ ★ 必须真的存在 ⇒ 否则这个臂是空的）
  target_line=""
  while IFS=$'\t' read -r nid nst nln; do
    [[ -n "$nid" ]] || continue
    mst=$(printf '%s\n' "$MAIN_REG" | awk -F'\t' -v k="$nid" '$1==k{print $2; exit}')
    [[ -n "$mst" ]] || continue
    scls=$(state_class "$nst")
    [[ "$scls" == '?' ]] && continue
    target_line="$nln"; break
  done < <(sibling_bare_rows "$FP_DOC")
  if [[ -z "$target_line" ]]; then
    bad "变异臂无法进行（★ 对照档里没有可比行）"
  else
    mutant="$mut_dir/mutant.md"
    cp "$FP_DOC" "$mutant"
    mfp=0
    while IFS=$'\t' read -r nid nst nln nflag; do
      [[ -n "$nid" ]] || continue
      mst=$(printf '%s\n' "$MAIN_REG" | awk -F'\t' -v k="$nid" '$1==k{print $2; exit}')
      [[ -n "$mst" ]] || continue
      mcls=$(state_class "$mst")
      scls=$(state_class "$nst")
      [[ "$scls" == '?' ]] && continue
      [[ "$mcls" != "$scls" ]] && mfp=$((mfp + 1))
    done < <(sibling_bare_rows "$mutant" "$target_line")
    if [[ "$mfp" -ge 1 ]]; then
      ok "量具自己有牙：★ 注入第 $target_line 行状态后分歧数 0 → ${mfp}（★ 变异臂会红）"
    else
      bad "量具没有牙：★ 注入第 $target_line 行后分歧数仍为 0 ⇒ 本门恒绿"
    fi
  fi
else
  bad "无法做变异臂（★ 对照档缺失）"
fi

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
exit 0
