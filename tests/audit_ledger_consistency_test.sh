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
# 覆盖：
#   AC-1  **抽取器本身要有效**：主档里「状态列写了闭合」的登记项，
#         必须能抽出**已知闭合集** {F3, F21, F22, F23}。
#         ★ 这条把「抽取器没有退化成抽不到东西」变成**受门保护的断言**
#           —— 否则 AC-2 会因为「抽不到闭合项」而**恒绿**（假绿）。
#   AC-2  主档标为**已闭合**的 F-项，兄弟档**不得**仍写「❌ 登记」，
#         且该行**必须带闭合标记**（正向断言：只写「不得 ❌ 登记」的话，
#         「✅ 本轮已修」/「★ 维持登记」这类**既不是闭合也不是未闭合**的
#         改写会从门缝里漏过去）。
#   AC-3  主档标为**已闭合**的 F-项，兄弟档 §三 **必须仍有这一行**
#         （整行消失 ⇒ 分叉的另一种形态）。
#   AC-4  兄弟档 §六 的每一项**必须**带一个状态标记
#         （已闭合 / 仍开 / 维持登记 / 口径已订正）—— 否则又是一条
#         「看不出当前状态」的登记项。
#   AC-5  兄弟档头部不得再宣称「待修正清单：N 项」而与 §六 的实际条目脱节；
#         且 §六 的条目数**不得少于棘轮下限**（历史记录逐条原文保留 ⇒
#         只能增、不能减）。
#   AC-6  **双向等价**：主档标「仍开」的 F-项集合 ≡ 兄弟档 §六 标「仍开」
#         的 F-项集合（只就 F-编号而言；「部署/push」这类非登记项
#         不参与比对）⇒ 防「一边说闭合、一边说仍开」。
#   AC-7  兄弟档里的「本档 §X」引用**必须在本档真实存在**
#         ⇒ 防悬空引用（★ 本轮实测抓到 5 处：上一轮改兄弟档登记册时，
#         把主档的章节号顺手抄进了「本档 §」，而兄弟档只到 §七）。
#
# ★ 编号口径：主档写 `F-21`、兄弟档写 `F21` ⇒ 全部**去连字符**后比对。
#   （主档 §二十.6 的编号撞车已在第十八轮重编号为 F-33 / F-34 修掉；
#     本门用到的 F-3 / F-21 / F-22 / F-23 两档语义对齐 —— 主档
#     §二十.7 明写「编号同兄弟档」。）
#
# ★ 本门**不需要** bash4 特性（无关联数组、无 mapfile）⇒ macOS 自带
#   bash 3.2 也能跑 ⇒ 可直接挂进 `make guards-deploy` / `guards-fast`。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAIN_DOC="$REPO_ROOT/docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md"
SIB_DOC="$REPO_ROOT/docs/12小时内修订审计-2026-11-02-0350.md"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
bad() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }

[[ -f "$MAIN_DOC" ]] || { echo "missing $MAIN_DOC" >&2; exit 1; }
[[ -f "$SIB_DOC" ]] || { echo "missing $SIB_DOC" >&2; exit 1; }

# 编号归一（去连字符）：主档 F-21 ⇒ F21，与兄弟档同形。
norm() { tr -d '-' | tr '[:lower:]' '[:upper:]'; }

# ── 主档登记册抽取 ─────────────────────────────────────────────────────
#
# ★ 只看**状态列**（第 2 列，awk -F'|' 的 $3）：正文散文里出现「闭合」
#   二字的行不该被算成闭合登记项。
# ★ 只从**登记项列**（$2）取编号：状态列的说明文字里若提到别的编号，
#   不应把它算成本行的编号。
closure_ids=$(awk -F'|' '$3 ~ /闭合/ {print $2}' "$MAIN_DOC" \
  | grep -oE 'F-?[0-9]+' | norm | sort -u)
open_ids=$(awk -F'|' '$3 ~ /仍开/ {print $2}' "$MAIN_DOC" \
  | grep -oE 'F-?[0-9]+' | norm | sort -u)

# ── AC-1：抽取器有效性（分母锚在**已知闭合集**上）──────────────────
#
# ★ 为什么必须显式断言这一条：AC-2 的语义是「主档闭合 ⇒ 兄弟档不得
#   仍写 ❌ 登记」。若抽取器因为措辞变化而抽不到闭合项，AC-2 会因为
#   **分母为 0** 而恒绿 —— 一道永远绿的门比没有门更危险。
#   ⇒ 这里把「已知闭合集必须抽得到」钉成断言，抽取器一退化门就红。
KNOWN_CLOSED="F3 F21 F22 F23"
if [[ -z "$closure_ids" ]]; then
  bad "主档里没有读到任何「状态列写了闭合」的登记项 ⇒ 抽取器或文档结构变了"
fi
for id in $KNOWN_CLOSED; do
  if printf '%s\n' "$closure_ids" | grep -qx "$id"; then
    ok
  else
    bad "已知闭合项 ${id} 没被抽取到（抽到的：$(printf '%s ' $closure_ids)）⇒ 抽取器口径退化"
  fi
done

# ── AC-2 / AC-3：主档闭合 ⇒ 兄弟档不得仍登记、也不得整行消失 ────────
for id in $closure_ids; do
  if grep -E "^\| \*\*${id}\*\* \|.*❌ 登记" "$SIB_DOC" >/dev/null 2>&1; then
    bad "兄弟档仍把 ${id} 标成「❌ 登记」，但主档已标为闭合 ⇒ 两份登记册分叉"
  else
    ok
  fi
  # 正向断言：该行必须**自己写了闭合**（否则「✅ 本轮已修」「★ 维持登记」
  # 这类既非闭合也非未闭合的改写会从门缝里漏过去）。
  if grep -E "^\| \*\*${id}\*\* \|" "$SIB_DOC" | grep -q "闭合"; then
    ok
  else
    bad "兄弟档 §三 的 ${id} 行没有闭合标记，但主档已标为闭合 ⇒ 两份登记册分叉"
  fi
  if grep -qE "^\| \*\*${id}\*\* \|" "$SIB_DOC"; then
    ok
  else
    bad "兄弟档 §三 里找不到 ${id} 这一行（整行消失 ⇒ 登记册分叉的另一种形态）"
  fi
done

# ── 兄弟档 §六 的区间 ───────────────────────────────────────────────
sec6_start=$(grep -n '^## 六、待修正清单' "$SIB_DOC" | head -1 | cut -d: -f1)
if [[ -z "$sec6_start" ]]; then
  bad "找不到兄弟档 §六 的起始标题"
  echo "PASS=$PASS FAIL=$FAIL"
  exit 1
fi
sec6_end=$(grep -n '^## ' "$SIB_DOC" | awk -F: -v s="$sec6_start" '$1>s{print $1; exit}')
[[ -n "$sec6_end" ]] || { bad "找不到兄弟档 §六 的结束标题"; echo "PASS=$PASS FAIL=$FAIL"; exit 1; }

# ── AC-4：§六 每一项必须带状态标记 ──────────────────────────────────
# ── AC-6：§六 标「仍开」的项，主档也必须标「仍开」（双向）──────────
item_total=0
item_marked=0
sib_open_ids=""
while IFS= read -r line; do
  [[ "$line" =~ ^[0-9]+\.\  ]] || continue
  item_total=$((item_total + 1))
  if echo "$line" | grep -qE '★ \*\*(已闭合|仍开|维持登记|口径已订正)\*\*'; then
    item_marked=$((item_marked + 1))
  else
    bad "兄弟档 §六 有一项没有状态标记：${line:0:60}"
  fi
  # AC-6：标「仍开」的项，其编号必须在主档的「仍开」集合里。
  if echo "$line" | grep -qE '★ \*\*仍开\*\*'; then
    oid=$(printf '%s\n' "$line" | grep -oE 'F-?[0-9]+' | head -1 | norm)
    if [[ -n "$oid" ]]; then
      sib_open_ids="$sib_open_ids $oid"
    else
      bad "兄弟档 §六 有一条「仍开」项但读不出编号：${line:0:60}"
    fi
  fi
done < <(sed -n "${sec6_start},${sec6_end}p" "$SIB_DOC")

if [[ "$item_total" -gt 0 && "$item_total" == "$item_marked" ]]; then
  ok
else
  bad "兄弟档 §六 的条目数（${item_total}）与带状态标记的条目数（${item_marked}）不一致"
fi

for oid in $sib_open_ids; do
  if printf '%s\n' "$open_ids" | grep -qx "$oid"; then
    ok
  else
    bad "兄弟档 §六 把 ${oid} 标成「仍开」，但主档登记册里它不是「仍开」（主档仍开集合：$(printf '%s ' $open_ids)）⇒ 两份登记册分叉"
  fi
done
# 反向：主档说「仍开」的项，兄弟档 §六 也必须说「仍开」
# （否则「兄弟档把它写成已闭合」这一侧的分叉会从门缝里漏过去）。
for oid in $open_ids; do
  if printf '%s\n' $sib_open_ids | grep -qx "$oid"; then
    ok
  else
    bad "主档把 ${oid} 标成「仍开」，但兄弟档 §六 没把它标成「仍开」（兄弟档仍开集合：$(printf '%s ' $sib_open_ids)）⇒ 两份登记册分叉"
  fi
done

# ── AC-5：头部声明的项数 == §六 实际条目数；且条目数不得少于棘轮下限 ──
#
# ★ 棘轮下限 23 = 2026-11-02 复核时的实测条目数。兄弟档 §六 明写
#   「历史记录，逐条原文保留」⇒ 条目**只能增不能减** ⇒ 少于 23 即分叉。
MIN_ITEMS=23
declared=$(grep -oE '待修正清单：\*\*[0-9]+ 项\*\*' "$SIB_DOC" | head -1 | grep -oE '[0-9]+')
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
#
# ★ 本轮实测：兄弟档里有 5 处「本档 §二十二 / §二十三 / §二十四」——
#   那是**兄弟档自己**没有的章节（只有主档有）⇒ 悬空引用 ⇒ 文档说了假话。
#   成因：上一轮改兄弟档登记册时，把主档的章节号顺手抄进了「本档 §」。
#
# ★ 解析全部用**字面量**模式（`§`、`）` 等非 ASCII 字符按字节精确匹配），
#   不用字符类 ⇒ 与 locale 无关（LC_ALL=C 下行为相同）。
sib_has_section() {
  local sec="$1"
  local top="${sec%%.*}"
  # 顶级节：`## 七、通用式…`
  if grep -q "^## ${top}、" "$SIB_DOC"; then return 0; fi
  # 子节：`### P1（下一轮优先）` / `### A. 4xx 归因族（errorsx）`
  if grep -q "^### ${sec} " "$SIB_DOC"; then return 0; fi
  return 1
}

# ★ 量具自检：解析器本身必须能分辨「存在」与「不存在」。
#   否则「一条引用都没有」会让本条在**读数上**失去意义
#   （分母为 0 的老陷阱在这里是「判别器恒返回同一个答案」）。
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

for ref in $(extract_self_refs "$SIB_DOC"); do
  [[ -n "$ref" ]] || continue
  if sib_has_section "$ref"; then
    ok
  else
    bad "兄弟档引用了「本档 §${ref}」，但本档没有这一节 ⇒ 悬空引用（文档说了假话）"
  fi
done

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]