#!/usr/bin/env bash
# tests/migration_retired_marker_contract_test.sh ——
#   「部署前的迁移退役判据不得静默丢弃本仓迁移」的行为门。
#
# ★ 2026-11-05 新增；★ 起因是 245/154 部署的**真实故障**：
#   共享部署库（deploy-lib/db-changelog.sh）的退役判据原写法是
#   「**前 15 行里出现 superseded / superceded / deprecated 三个词就跳过**」
#   ⇒ 任何在表头里提到这些**业务词汇**的迁移都会被**静默丢弃**：
#     · 944_sales_quotes.sql（表头图例「→ won | lost；superseded（版本被新版本替代时…）」）
#     · 945_sales_quote_versions.sql（表头「superseded_by（Kill Bill 版本序列…）」）
#   ⇒ 这两条被跳过 ⇒ sales_quotes / sales_quote_versions **两张表从未建出来**
#   ⇒ 946 的外键当场失败：
#       ERROR: relation "public.sales_quote_versions" does not exist
#   ⇒ 245/154 部署在「切换前 pending 迁移」这步**中止**（未切符号链接 ⇒ 线上仍是旧版）。
#
# ★ 本门钉住三条：
#   ①  **属性**：★ 本仓的 startup 迁移**不得**被退役判据**静默丢弃**
#     （判据取**共享库的真实现**；共享库不可达时用**规定口径**⇒ 明说走了哪条臂）。
#   ②  **语义自测**：★ 退役判据必须**区分「标记」与「提及」**：
#     显式标记行（`-- SUPERSEDED: <原因>` / `-- DEPRECATED: …`）⇒ 退役；
#     标识符（`superseded_by`）、业务词汇（正文里的 superseded）、DDL 行 ⇒ **不是**退役。
#   ③  **单点真源**：★ 共享库里**不得**残留「子串匹配」式的退役判据
#     （两处调用点都必须走同一个 helper）⇒ 防「只修了一处、另一处仍误伤」。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# Run: bash tests/migration_retired_marker_contract_test.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); printf 'OK   %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1" >&2; }
skip() { SKIP=$((SKIP + 1)); printf 'SKIP %s\n' "$1"; }

# ── 规定口径（CI 无共享库时用这条臂；与共享库的 helper 同语义）──────────────
# ★ 口径：**标记词必须是该注释行的第一个词** + 词边界（`superseded_by` 不算）。
_specified_marker_re() {
  grep -qiE '^[[:space:]]*--[[:space:]]*[-*>[:space:]]*\**(SUPERSEDED|SUPERCEDED|DEPRECATED)([^[:alnum:]_]|$)'
}

# ── 共享库定位（★ 2026-11-09 F51：★ 改用共享的唯一真相候选链 ★）──────────
# ★ 本段此前**自带**一条候选链 ⇒ ★ 与两个兄弟解析器各写各的 ⇒ ★ 现在共用
#   scripts/deploy-lib-candidates.sh ⇒ ★ 链只有一条、★ 各门不会「测另一条链」★
# shellcheck source=deploy-lib-candidates.sh
source "$REPO_ROOT/scripts/deploy-lib-candidates.sh"
SSOT=""
if _ssot_cand="$(deploy_lib_find db-changelog.sh)"; then
  SSOT="$_ssot_cand/db-changelog.sh"
fi

# ── 判据实现：优先用共享库的【真实现】（有牙齿），否则用规定口径 ────────────
MODE="specified"
if [[ -n "$SSOT" ]] && bash -c 'set -euo pipefail; source "$1"; declare -F _deploy_migration_has_retired_marker >/dev/null' _ "$SSOT" 2>/dev/null; then
  MODE="ssot"
  ok "① judge = shared SSOT helper ($SSOT)"
else
  skip "① shared SSOT helper unreachable → using the SPECIFIED semantics (CI 环境无共享库)"
fi

marker_says_retired() { # <file>
  if [[ "$MODE" == "ssot" ]]; then
    bash -c 'set -euo pipefail; source "$1"; _deploy_migration_has_retired_marker "$2"' _ "$SSOT" "$1"
  else
    head -15 "$1" | _specified_marker_re
  fi
}

# ── ① 属性：本仓 startup 迁移不得被退役判据静默丢弃 ────────────────────────
# ★ 只查 **>= 412** 的迁移（部署侧 pending 发现的判定阈值；
#   DB_LEDGER_RECONCILE_FROM 默认 412 ⇒ 更低版本不在该路径的处置范围内）。
count_checked=0
dropped=""
for f in sql/migrations/startup/[0-9]*.sql; do
  [[ -f "$f" ]] || continue
  base=$(basename "$f")
  [[ "$base" == *.down.sql ]] && continue
  [[ "$base" == *.skip ]] && continue
  [[ "$base" == *.bak.skip ]] && continue
  ver=$(echo "$base" | grep -oE '^[0-9]+' || true)
  [[ -n "$ver" ]] || continue
  (( 10#$ver >= 412 )) || continue
  count_checked=$((count_checked + 1))
  if marker_says_retired "$f"; then dropped="$dropped $base"; fi
done
if [[ -z "$dropped" ]]; then
  ok "① no startup migration (>=412, $count_checked checked) is silently dropped by the retirement judge"
else
  bad "① the retirement judge silently drops:${dropped}"
  printf '     ⇒ those migrations would NEVER be applied ⇒ their tables/FKs never exist\n' >&2
fi

# ── ② 语义自测：判据必须区分「标记」与「提及」 ─────────────────────────────
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mk() { printf '%s\n' "$@" > "$TMP/probe.sql"; }

mk '-- SUPERSEDED: 与 999 合并' \
   '-- 说明'
if marker_says_retired "$TMP/probe.sql"; then
  ok "②a explicit marker line => retired"
else
  bad "②a explicit marker line => NOT recognised as retired (the judge regressed to substring matching)"
fi

mk '-- 说明：这里提到 superseded_by 这个列名' \
   '-- 第二行'
if marker_says_retired "$TMP/probe.sql"; then
  bad "②b identifier superseded_by => wrongly judged as retired"
else
  ok "②b identifier superseded_by => not retired"
fi

mk '-- 图例：→ won | lost；superseded（版本被新版本替代时）' \
   '-- 第二行'
if marker_says_retired "$TMP/probe.sql"; then
  bad "②c prose mention of superseded => wrongly judged as retired"
else
  ok "②c prose mention of superseded => not retired"
fi

mk '    ADD COLUMN IF NOT EXISTS superseded_by VARCHAR(128);' \
   '-- 说明'
if marker_says_retired "$TMP/probe.sql"; then
  bad "②d DDL line containing superseded_by => wrongly judged as retired"
else
  ok "②d DDL line containing superseded_by => not retired"
fi

mk '-- DEPRECATED: 已被 812 取代' \
   '-- 说明'
if marker_says_retired "$TMP/probe.sql"; then
  ok "②e explicit DEPRECATED marker => retired"
else
  bad "②e explicit DEPRECATED marker => NOT recognised as retired"
fi

# ── ③ 单点真源：共享库里不得残留「子串匹配」式的退役判据 ───────────────────
if [[ "$MODE" == "ssot" ]]; then
  # ★ 残留判据的形态：head -N … | grep -qiE 'SUPERSEDED|superceded|DEPRECATED'
  #   （**没有**锚点 ⇒ 子串匹配 ⇒ 会被业务词汇误伤）。
  if grep -nE "grep -qiE '[^']*SUPERSEDED" "$SSOT" | grep -qE 'SUPERSEDED\|superceded\|DEPRECATED'; then
    bad "③ the shared SSOT still carries a substring-matching retirement judge"
    grep -nE "grep -qiE '[^']*SUPERSEDED" "$SSOT" >&2
  else
    ok "③ the shared SSOT has no substring-matching retirement judge left"
  fi
else
  skip "③ shared SSOT unreachable → cannot audit its call sites"
fi

printf 'migration_retired_marker selftest: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[[ $FAIL -eq 0 ]]
