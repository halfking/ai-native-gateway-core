#!/usr/bin/env bash
# =====================================================================
# tests/audit_ledger_consistency_test.sh — 两份审计档的**登记册状态**必须一致
#
# ★ 2026-11-02 第二十二轮：
#   两份审计档各有一份登记册：
#     · 主档 docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md
#     · 兄弟档 docs/12小时内修订审计-2026-11-02-0350.md
#   ⇒ **一份更新了、另一份没更新**，是**静默分叉**：后来者照着旧登记册
#     会把**已闭合**的项重新开一遍（而那项的「重新实现」往往会把
#     后来者引回被否过的方向）。
#   ⇒ 本轮实测：兄弟档 §三 表 20 行 + §六 清单 23 项 + 头部计数
#     **全部停在旧状态**（已闭合的仍写「❌ 登记」）⇒ 逐条订正。
#
# ★★★★ 2026-11-03 第三十六轮：**这道门曾经「绿着漏」** ⇒ ★ 本轮重写抽取口径。
#
# ★ ★★ 实测到的根因（★ 不是猜的）：★ 本门原来的闭合集抽取是
#
#     awk -F'|' '$3 ~ /闭合/ {print $2}'
#
#   ⇒ ★★ 它**只认「闭合」两个字** ⇒ ⇒ 「**已关闭**」「**已实施**」「**已落地**」
#   以及第三态「**有意如此**」**在门里根本不存在**
#   ⇒ ⇒ 第三十三轮的核心结论（F13 已关闭、F28/F29/F30 有意如此）
#   ⇒ ⇒ **跨文档门从一开始就没看见**（★ 实测盲区 = 规范源 18 条里的 **6 条 = 33.3%**）。
#
# ★ ⇒ ⇒ 本轮把口径改成**按「状态类」比**，★ 并新增两条判据：
#   AC-8 ★ **词表完整性**：★ 规范源里**已知的**每一类状态都必须被抽到
#         ⇒ ⇒ ★ 这条**直接**钉住上面那个词表盲区（★ 词表一退化就红）。
#   AC-9 ★ **三态等价**：★ 兄弟档的**状态类**必须与规范源**一致**
#         ⇒ ⇒ ★ 而**不是**只比「闭合 / 未闭合」（★ 第三态在二元口径里没有位置）。
#
# 覆盖：
#   AC-1  **抽取器本身要有效**：规范源（§28.23）必须抽得到、条目数 ≥ 30、
#         且已知闭合集 {F3, F21, F22, F23} 必须被抽成 closed。
#         ★ 这条把「抽取器没有退化成抽不到东西」变成**受门保护的断言**
#           —— 否则 AC-2/AC-9 会因为「抽不到闭合项」而**恒绿**（假绿）。
#   AC-2  主档标为**已闭合**的 F-项，兄弟档**不得**仍写「❌ 登记」，
#         且该行**必须带闭合标记**（正向断言）。
#   AC-3  兄弟档 §三 **必须仍有这一行**（整行消失 ⇒ 分叉的另一种形态）。
#         ★ 只对**兄弟档自己登记的**编号生效（★ 主档独有的 F-A / F33 / F34
#         在兄弟档里本来就没有 ⇒ 不是分叉）。
#   AC-4  兄弟档 §六 的每一项**必须**带一个**规范词表**里的状态标记
#         （已闭合 / 已关闭 / 已实施 / 已落地 / 有意如此 / 仍开）。
#   AC-5  兄弟档头部不得再宣称「待修正清单：N 项」而与 §六 的实际条目脱节；
#         且 §六 的条目数**不得少于棘轮下限**（历史记录逐条原文保留 ⇒
#         只能增、不能减）。
#   AC-6  **双向等价**：主档标「仍开」的 F-项 ≡ 兄弟档 §六 标「仍开」的 F-项。
#   AC-7  兄弟档里的「本档 §X」引用**必须在本档真实存在** ⇒ 防悬空引用。
#   AC-8  **词表完整性**（★ 第三十六轮新增）。
#   AC-9  **三态等价**（★ 第三十六轮新增）。
#
# ★ 编号口径：主档写 `F-21`、兄弟档写 `F21` ⇒ 全部**去连字符**后比对。
#   （主档 §二十.6 的编号撞车已在第十八轮重编号为 F-33 / F-34 修掉。）
#
# ★ 本门**不需要** bash4 特性（无关联数组、无 mapfile）⇒ macOS 自带
#   bash 3.2 也能跑 ⇒ 可直接挂进 `make guards-deploy` / `guards-fast`。
#
# ★ ★★ `--selftest`（★ 第三十六轮新增）：★ 本门自己也要有牙。
#   ⇒ ★ 它在**临时副本**上注入一处**已知的分叉**，★ 然后断言**自己必须报出来**
#   ⇒ ⇒ ★★ 一道「永远不会红」的门比没有门更危险（★ 实测过：旧门就是这样绿的）。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAIN_DOC="${AUDIT_MAIN_DOC:-$REPO_ROOT/docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md}"
SIB_DOC="${AUDIT_SIB_DOC:-$REPO_ROOT/docs/12小时内修订审计-2026-11-02-0350.md}"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
bad() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }

# 编号归一（去连字符）：主档 F-21 ⇒ F21，与兄弟档同形。
norm() { tr -d '-' | tr '[:lower:]' '[:upper:]'; }

# ── 状态类判定（★ 词表必须**完整**：★ 旧门只认「闭合」⇒ 盲区 33.3%）────
#
# closed      = 已闭合 / 已关闭 / 已实施 / 已落地
# intentional = 有意如此（★ 第三种状态：★ 不是缺陷、也不是待办）
# open        = 仍开
state_class() {
  local t="$1"
  local c=""
  case "$t" in *闭合*|*已关闭*|*已实施*|*已落地*) c="closed" ;; esac
  case "$t" in *有意如此*) [ -n "$c" ] && c="mixed" || c="intentional" ;; esac
  case "$t" in *仍开*) [ -n "$c" ] && c="mixed" || c="open" ;; esac
  printf '%s' "$c"
}

# ── 抽取：规范源（主档 §28.23）─────────────────────────────────────
# 输出：ID<TAB>class<TAB>lineno
extract_canon() {
  local doc="$1"
  local start end ln id st cls
  # ★ 定位必须**同时**命中两个词，★ 但**顺序不限**（★ 真实标题是
  #   「### 28.23 登记项现状总表（★ **规范源**）」⇒ ★ 「现状总表」在前）。
  #   ⇒ ★ 只命中一个词 ⇒ ★ 轮次记录里的一句「建**规范源**」就会把它误判成规范源。
  start=$(grep -nE '^### .*规范源.*现状总表|^### .*现状总表.*规范源' "$doc" | head -1 | cut -d: -f1)
  [[ -n "$start" ]] || return 1
  end=$(awk -v s="$start" 'NR>s && /^#{2,4} /{print NR; exit}' "$doc")
  [[ -n "$end" ]] || end=$(wc -l < "$doc")
  ln="$start"
  while [[ "$ln" -lt "$end" ]]; do
    ln=$((ln + 1))
    local line
    line=$(sed -n "${ln}p" "$doc")
    case "$line" in "|"*) ;; *) continue ;; esac
    # 第一格 = 登记项编号
    id=$(printf '%s\n' "$line" | awk -F'|' '{print $2}' | tr -d '*' | tr -d ' ' | norm)
    case "$id" in F[0-9]*|F[A-Z]) ;; *) continue ;; esac
    # 第三格 = 现状
    st=$(printf '%s\n' "$line" | awk -F'|' '{print $3}')
    cls=$(state_class "$st")
    printf '%s\t%s\t%s\n' "$id" "$cls" "$ln"
  done
}

# ── 抽取：兄弟档 §三 表（★ 状态格 = 第 6 格）─────────────────────────
# 输出：ID<TAB>class<TAB>lineno
extract_sib3() {
  local doc="$1" ln=0 line id st cls
  while IFS= read -r line; do
    ln=$((ln + 1))
    case "$line" in "|"*) ;; *) continue ;; esac
    id=$(printf '%s\n' "$line" | awk -F'|' '{print $2}' | tr -d '*' | tr -d ' ' | norm)
    case "$id" in F[0-9]*|F[A-Z]) ;; *) continue ;; esac
    st=$(printf '%s\n' "$line" | awk -F'|' '{print $6}')
    cls=$(state_class "$st")
    printf '%s\t%s\t%s\n' "$id" "$cls" "$ln"
  done < "$doc"
}

# ── 抽取：兄弟档 §六 清单（★ 状态标记 = `★ **状态**`）───────────────
# 输出：ID<TAB>class<TAB>lineno（★ 一个条目可覆盖**多个**编号 ⇒ 逐个输出）
extract_sib6() {
  local doc="$1"
  local start end ln=0 line marker cls ids id
  start=$(grep -n '^## 六、' "$doc" | head -1 | cut -d: -f1)
  [[ -n "$start" ]] || return 1
  end=$(awk -v s="$start" 'NR>s && /^## /{print NR; exit}' "$doc")
  [[ -n "$end" ]] || end=$(wc -l < "$doc")
  ln=0
  while IFS= read -r line; do
    ln=$((ln + 1))
    [[ "$ln" -ge "$start" && "$ln" -lt "$end" ]] || continue
    printf '%s\n' "$line" | grep -qE '^[0-9]+\. ' || continue
    marker=$(printf '%s\n' "$line" | grep -oE '★ \*\*[^*]+\*\*' | head -1 | sed 's/★ \*\*//; s/\*\*//')
    cls=$(state_class "$marker")
    ids=$(printf '%s\n' "$line" | grep -oE 'F-?[0-9]+|F-[A-Z]' | norm | sort -u)
    for id in $ids; do
      printf '%s\t%s\t%s\n' "$id" "$cls" "$ln"
    done
  done < "$doc"
}

# ── 主流程：跑全部判据 ─────────────────────────────────────────────
run_checks() {
  local main="$1" sib="$2"
  local canon canon_ids canon_closed canon_intent canon_open
  local s3 s6 sib_ids

  canon=$(extract_canon "$main")
  s3=$(extract_sib3 "$sib")
  s6=$(extract_sib6 "$sib")

  canon_ids=$(printf '%s\n' "$canon" | cut -f1 | sort -u)
  canon_closed=$(printf '%s\n' "$canon" | awk -F'\t' '$2=="closed"{print $1}' | sort -u)
  canon_intent=$(printf '%s\n' "$canon" | awk -F'\t' '$2=="intentional"{print $1}' | sort -u)
  canon_open=$(printf '%s\n' "$canon" | awk -F'\t' '$2=="open"{print $1}' | sort -u)
  sib_ids=$(printf '%s\n' "$s3
%s" "$s6" | cut -f1 | sort -u)

  # ── AC-1：抽取器有效性（分母锚在**已知闭合集**上）──────────────────
  #
  # ★ 为什么必须显式断言这一条：AC-2 / AC-9 的语义是「主档的现状 ⇒ 兄弟档
  #   不得不同」。若抽取器因为措辞变化而抽不到闭合项，那些检查会因为
  #   **分母为 0** 而恒绿 —— 一道永远绿的门比没有门更危险。
  if [[ -z "$canon_ids" ]]; then
    bad "主档里没有读到任何规范源登记项 ⇒ 抽取器或文档结构变了"
  fi
  local canon_n
  canon_n=$(printf '%s\n' "$canon_ids" | grep -c . )
  if [[ "$canon_n" -ge 30 ]]; then
    ok
  else
    bad "规范源条目数 ${canon_n} < 30（★ 第三十六轮实测值 = 39）⇒ 抽取器可能退化了"
  fi
  for id in F3 F21 F22 F23; do
    if printf '%s\n' "$canon_closed" | grep -qx "$id"; then
      ok
    else
      bad "已知闭合项 ${id} 没被抽成 closed（抽到的：$(printf '%s ' $canon_closed)）⇒ 抽取器口径退化"
    fi
  done

  # ── AC-8：★ 词表完整性（★ 第三十六轮新增）────────────────────────
  #
  # ★ 这条**直接**钉住旧门的词表盲区：★ 旧门只认「闭合」⇒ 「已关闭」
  #   「已实施」「已落地」「有意如此」**在门里不存在** ⇒ 实测盲区 33.3%。
  for id in F13 F32 FB; do
    if printf '%s\n' "$canon_closed" | grep -qx "$id"; then
      ok
    else
      bad "规范源里的 ${id}（闭合类：已关闭/已落地/已实施）**没被抽成 closed** ⇒ ★ 状态词表退化（★ 这正是旧门的盲区）"
    fi
  done
  for id in F28 F29 F30 F31; do
    if printf '%s\n' "$canon_intent" | grep -qx "$id"; then
      ok
    else
      bad "规范源里的 ${id}（第三态：有意如此）**没被抽成 intentional** ⇒ ★ 状态词表退化（★ 第三态在二元口径里没有位置）"
    fi
  done
  if printf '%s\n' "$canon_open" | grep -qx "F7"; then
    ok
  else
    bad "规范源里的 F7（仍开）没被抽成 open ⇒ ★ 状态词表退化"
  fi

  # ── AC-2：主档闭合 ⇒ 兄弟档不得仍登记、也不得整行消失 ──────────────
  #
  # ★ 只对**兄弟档自己登记的**编号生效（★ 主档独有的 F-A / F33 / F34
  #   在兄弟档里本来就没有 ⇒ 「整行消失」不是它们的分叉形态）。
  for id in $canon_closed; do
    printf '%s\n' "$sib_ids" | grep -qx "$id" || continue
    if grep -E "^\| \*\*${id}\*\* \|.*❌ 登记" "$sib" >/dev/null 2>&1; then
      bad "兄弟档仍把 ${id} 标成「❌ 登记」，但主档已标为闭合 ⇒ 两份登记册分叉"
    else
      ok
    fi
    # 正向断言：该行必须**自己写了闭合**（否则「✅ R1」「★ 维持登记」
    # 这类既非闭合也非未闭合的改写会从门缝里漏过去）。
    if grep -E "^\| \*\*${id}\*\* \|" "$sib" | grep -qE '(闭合|已关闭|已实施|已落地)'; then
      ok
    else
      bad "兄弟档 §三 的 ${id} 行没有闭合标记，但主档已标为闭合 ⇒ 两份登记册分叉"
    fi
    # AC-3：该行不得整行消失
    if grep -qE "^\| \*\*${id}\*\* \|" "$sib"; then
      ok
    else
      bad "兄弟档 §三 里找不到 ${id} 这一行（整行消失 ⇒ 登记册分叉的另一种形态）"
    fi
  done

  # ── AC-4：§六 每一项必须带**规范词表**里的状态标记 ─────────────────
  # ── AC-6：§六 标「仍开」的项，主档也必须标「仍开」（双向）──────────
  local sec6_start sec6_end item_total item_marked sib_open_ids
  item_total=0
  item_marked=0
  sib_open_ids=""
  sec6_start=$(grep -n '^## 六、待修正清单' "$sib" | head -1 | cut -d: -f1)
  if [[ -z "$sec6_start" ]]; then
    bad "找不到兄弟档 §六 的起始标题"
    echo "PASS=$PASS FAIL=$FAIL"
    return 1
  fi
  sec6_end=$(grep -n '^## ' "$sib" | awk -F: -v s="$sec6_start" '$1>s{print $1; exit}')
  [[ -n "$sec6_end" ]] || { bad "找不到兄弟档 §六 的结束标题"; echo "PASS=$PASS FAIL=$FAIL"; return 1; }

  while IFS= read -r line; do
    [[ "$line" =~ ^[0-9]+\.\  ]] || continue
    item_total=$((item_total + 1))
    if echo "$line" | grep -qE '★ \*\*(已闭合|已关闭|已实施|已落地|有意如此|仍开)\*\*'; then
      item_marked=$((item_marked + 1))
    else
      bad "兄弟档 §六 有一项没有规范状态标记：${line:0:60}"
    fi
    if echo "$line" | grep -qE '★ \*\*仍开\*\*'; then
      oid=$(printf '%s\n' "$line" | grep -oE 'F-?[0-9]+|F-[A-Z]' | head -1 | norm)
      if [[ -n "$oid" ]]; then
        sib_open_ids="$sib_open_ids $oid"
      else
        bad "兄弟档 §六 有一条「仍开」项但读不出编号：${line:0:60}"
      fi
    fi
  done < <(sed -n "${sec6_start},${sec6_end}p" "$sib")

  if [[ "$item_total" -gt 0 && "$item_total" == "$item_marked" ]]; then
    ok
  else
    bad "兄弟档 §六 的条目数（${item_total}）与带状态标记的条目数（${item_marked}）不一致"
  fi

  for oid in $sib_open_ids; do
    if printf '%s\n' "$canon_open" | grep -qx "$oid"; then
      ok
    else
      bad "兄弟档 §六 把 ${oid} 标成「仍开」，但主档登记册里它不是「仍开」（主档仍开集合：$(printf '%s ' $canon_open)）⇒ 两份登记册分叉"
    fi
  done
  for oid in $canon_open; do
    printf '%s\n' "$sib_ids" | grep -qx "$oid" || continue
    if printf '%s\n' $sib_open_ids | grep -qx "$oid"; then
      ok
    else
      bad "主档把 ${oid} 标成「仍开」，但兄弟档 §六 没把它标成「仍开」（兄弟档仍开集合：$(printf '%s ' $sib_open_ids)）⇒ 两份登记册分叉"
    fi
  done

  # ── AC-9：★★ 三态等价（★ 第三十六轮新增）─────────────────────────
  #
  # ★★ 这条是本轮的核心：★ 旧门是**二元**的（闭合 / 没闭合）⇒ ⇒ 第三态
  #   「有意如此」**在门里没有位置** ⇒ ⇒ 「绿门 + 真分叉」。
  # ⇒ ⇒ 这里比的是**状态类**：★ closed / intentional / open 必须一一对应。
  local eid ecls eline ccls
  while IFS=$'\t' read -r eid ecls eline; do
    [[ -n "$eid" ]] || continue
    ccls=$(printf '%s\n' "$canon" | awk -F'\t' -v k="$eid" '$1==k{print $2; exit}')
    [[ -n "$ccls" ]] || continue
    if [[ "$ecls" == "mixed" ]]; then
      bad "兄弟档 ${eid} 的状态标记同时含**多个**状态类（行 ${eline}）⇒ 自相矛盾"
    elif [[ "$ecls" != "$ccls" ]]; then
      bad "兄弟档 ${eid} 的状态类 = 「${ecls:-（不在规范词表内）}」，而规范源 = 「${ccls}」（行 ${eline}）⇒ ★ 两份登记册分叉"
    else
      ok
    fi
  done <<<"$s3
$s6"

  # ── AC-5：头部声明的项数 == §六 实际条目数；且条目数不得少于棘轮下限 ──
  #
  # ★ 棘轮下限 23 = 2026-11-02 复核时的实测条目数。兄弟档 §六 明写
  #   「历史记录，逐条原文保留」⇒ 条目**只能增不能减** ⇒ 少于 23 即分叉。
  local MIN_ITEMS=23 declared
  declared=$(grep -oE '待修正清单：\*\*[0-9]+ 项\*\*' "$sib" | head -1 | grep -oE '[0-9]+')
  if [[ -z "$declared" ]]; then
    bad "兄弟档头部找不到「待修正清单：N 项」的声明"
  elif [[ "$declared" == "$item_total" ]]; then
    ok
  else
    bad "兄弟档头部声明「待修正清单：${declared} 项」，§六 实际 ${item_total} 项 ⇒ 脱节"
  fi
  if [[ "$item_total" -ge "$MIN_ITEMS" ]]; then
    ok
  else
    bad "兄弟档 §六 只剩 ${item_total} 项，低于棘轮下限 ${MIN_ITEMS} 项（该清单是历史记录，只增不减）"
  fi

  # ── AC-7：兄弟档的「本档 §X」引用必须在本档真实存在 ─────────────────
  sib_has_section() {
    local sec="$1"
    local doc="${2:-$sib}"
    local top="${sec%%.*}"
    if grep -q "^## ${top}、" "$doc"; then return 0; fi
    if grep -q "^### ${sec} " "$doc"; then return 0; fi
    return 1
  }
  # ★ 量具自检：解析器本身必须能分辨「存在」与「不存在」。
  if sib_has_section "六" && ! sib_has_section "九十九"; then
    ok
  else
    bad "章节解析器失灵（存在/不存在的判别不一致）⇒ AC-7 的读数不可信"
  fi
  extract_self_refs() {
    grep -o '本档 §[^ ]*' "$1" | sed 's/^本档 §//' | while IFS= read -r t; do
      t="${t%%）*}"
      t="${t%%，*}"
      t="${t%%：*}"
      t="${t%%、*}"
      t="${t%%\)*}"
      printf '%s\n' "$t"
    done | sort -u
  }
  local ref
  for ref in $(extract_self_refs "$sib"); do
    [[ -n "$ref" ]] || continue
    if sib_has_section "$ref" "$sib"; then
      ok
    else
      bad "兄弟档引用了「本档 §${ref}」，但本档没有这一节 ⇒ 悬空引用（文档说了假话）"
    fi
  done

  echo "PASS=$PASS FAIL=$FAIL"
}

# ── `--selftest`：★ 本门自己也要有牙（★ 第三十六轮新增）────────────
#
# ★ 做法：在**临时副本**上注入一处**已知的分叉**（★ 把 F13 的状态标记改回
#   旧词「★ **维持登记**」⇒ ★ 那正是第三十三轮的真实缺陷形态），
#   ⇒ 然后断言**自己必须报出 F13** ⇒ ⇒ ★ 一道「永远不会红」的门比没有门更危险。
selftest() {
  local tmpdir
  tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/audit-ledger-selftest.XXXXXX") || return 1
  local out_before out_after
  PASS=0; FAIL=0
  out_before=$(run_checks "$MAIN_DOC" "$SIB_DOC" 2>&1)
  if [[ "$out_before" != *"FAIL=0"* ]]; then
    printf 'SELFTEST: 未注入前本门就不绿（%s）⇒ 先修文档再谈自检\n' "$out_before" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  # ★ 注入：把兄弟档的 F13 §三 行的状态格换成旧词
  local mutant="$tmpdir/sib.md"
  sed 's/^| \*\*F13\*\* | MINOR | \(.*\)| ★ \*\*已关闭\*\*[^|]*|/| **F13** | MINOR | \1| ★ **维持登记**（主档 §9.6）|/' \
    "$SIB_DOC" > "$mutant"
  if cmp -s "$SIB_DOC" "$mutant"; then
    printf 'SELFTEST: ★ 注入失败（★ 兄弟档里找不到 F13 的 §三 行）⇒ 夹具坏了\n' >&2
    rm -rf "$tmpdir"
    return 1
  fi
  PASS=0; FAIL=0
  out_after=$(run_checks "$MAIN_DOC" "$mutant" 2>&1)
  if [[ "$out_after" == *"FAIL=0"* ]]; then
    printf 'SELFTEST: ★★ **探测器坏了**：★ 注入了一处已知的分叉（★ F13 改回旧词「维持登记」），★ 本门却**没有**报出来\n' >&2
    rm -rf "$tmpdir"
    return 1
  fi
  if [[ "$out_after" != *"F13"* ]]; then
    printf 'SELFTEST: ★★ 门报出了违例，但**没有点名** F13（★ 报的是：%s）\n' "$out_after" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  rm -rf "$tmpdir"
  echo "SELFTEST: PASS（★ 注入前绿 / 注入后红且点名 F13 / 临时副本已清理）"
  return 0
}

# ── 入口 ─────────────────────────────────────────────────────────────
case "${1:-}" in
  --selftest)
    selftest
    exit $?
    ;;
esac

[[ -f "$MAIN_DOC" ]] || { echo "missing $MAIN_DOC" >&2; exit 1; }
[[ -f "$SIB_DOC" ]] || { echo "missing $SIB_DOC" >&2; exit 1; }

run_checks "$MAIN_DOC" "$SIB_DOC"
[[ $FAIL -eq 0 ]]
