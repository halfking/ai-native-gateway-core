#!/usr/bin/env bash
# maestro-id-selector-shadow-gate.sh —— 守住「Maestro flow 的 `id:` 选择器不被 title/aria-label 遮蔽」
#
# ## 为什么这道门存在（L13 补轮 · 2026-11-23）
#
# ★ 本轮实测钉死了 Maestro web 驱动的一条**选择器语义**：
#
#   Maestro 的 `id:` 选择器匹配的是 hierarchy 的 `resource-id`；
#   而 web 驱动取 `resource-id` 时，**`aria-label` / `title` 会盖掉 `data-testid`**。
#
# ★ 实测证据（同一台机器、同一次会话）：
#   · `<button data-testid="sa-tab-models">`（**无** title/aria-label）
#       ⇒ hierarchy 的 resource-id = `sa-tab-models` ⇒ `id:` **能**命中（真机 COMPLETED）
#   · `<button data-testid="stage-stats-open" :title="…">`
#       ⇒ hierarchy 的 resource-id = **「展开统计（全页）」**（= title 的值）⇒ `id:` **不能**命中
#   · `<button data-testid="work-mode-switch" :title="…" :aria-label="…">`
#       ⇒ hierarchy 的 resource-id = **「切到简易模式」**（= aria-label 的值）⇒ `id:` **不能**命中
#
# ⇒ ★★★ 所以「`<button>` 选不到」这个结论**是错的** ——
#   判别因素**不是元素类型**，而是「**有没有 title/aria-label 盖住 data-testid**」。
#   ⇒ 本仓 `maestro/README.md` §6.6 那条 2026-11-17 的记载把两个个例**推广成了类型规律**
#   ⇒ ★ 而它一直在挡路（本轮 L13 差点因此放弃把 tab 交互从中文文案改成 data-testid）。
#
# ⇒ 一个被遮蔽的 `id:` ⇒ 该断言**在真机上必然永远找不到** ⇒ **恒假红**
#   ⇒ 而且它是**静默**的（表现为「找不到元素」，很容易被误判成「页面坏了」）
#   ⇒ ⇒ 必须让它在**提交时**就红。
#
# ## 判据
#   1.【行为】真跑 `maestro/tools/audit-maestro-id-selectors.py` ⇒ 必须绿。
#   2.【对照臂】在**临时目录**里造一棵最小仓（一个带 `title` 的按钮 + 一个用 `id:` 选它的 flow）
#      ⇒ 工具**必须红**且**红在该 id 上**。
#      ★ 用 `--root=<临时目录>` 扫，⇒ **不改仓内任何文件**（门必须能在 CI 里跑）。
#      ★ 没有这一条，门可能就是「恒绿」的 —— 工具写错了也照样过。
#
# 用法：bash scripts/checks/maestro-id-selector-shadow-gate.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOL="maestro/tools/audit-maestro-id-selectors.py"
PASS=0
FAIL=0

ok()  { PASS=$((PASS + 1)); printf '[maestro-id] ok: %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '[maestro-id] FAIL: %s\n' "$1" >&2; }

if ! command -v python3 >/dev/null 2>&1; then
  bad "python3 不在 PATH —— 审计工具跑不了（工具不可运行 ⇒ 它的产出不可复现 ⇒ 门的价值归零）"
  printf '\n[maestro-id] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

if [[ ! -f "$TOOL" ]]; then
  bad "缺审计工具 ${TOOL}"
  printf '\n[maestro-id] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

# ── 1. 行为判据：真跑工具，仓内不得有被遮蔽的 id 选择器 ─────────────────
tool_out="$(python3 "$TOOL" --json 2>&1)"
tool_rc=$?
if [[ $tool_rc -eq 0 ]]; then
  ok "审计工具判定：仓内 flow 的 id 选择器未被 title/aria-label 遮蔽"
else
  bad "审计工具判定：存在被遮蔽的 id 选择器（⇒ 该断言在真机上恒假红）——"
  printf '%s\n' "$tool_out" | sed 's/^/[maestro-id]   /' >&2
fi

# ── 2/3. 对照臂：临时目录里造一棵最小仓 ⇒ 工具必须红，且红在该 id 上 ────
TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t maestro-id)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

mkdir -p "$TMP_DIR/maestro/flows" "$TMP_DIR/web/src"
cat >"$TMP_DIR/maestro/flows/_probe.yaml" <<'YAML'
- tapOn:
    id: "shadowed-btn"
YAML
cat >"$TMP_DIR/web/src/Probe.vue" <<'VUE'
<template>
  <button type="button" data-testid="shadowed-btn" title="展开统计（全页）" @click="go">展开</button>
</template>
VUE

arm_out="$(python3 "$TOOL" --root="$TMP_DIR" --json 2>&1)"
arm_rc=$?
if [[ $arm_rc -eq 0 ]]; then
  bad "对照臂：带 title 的按钮被 `id:` 选中时工具仍判绿 ⇒ 判据抓不住遮蔽（门恒绿）"
else
  ok "对照臂：带 title 的按钮存在时工具判红（退出码 ${arm_rc}）"
  if printf '%s' "$arm_out" | grep -q 'shadowed-btn'; then
    ok "对照臂：红在那个被遮蔽的 id 上（判据精确命中，不是随便红）"
  else
    bad "对照臂虽然红了，但没红在 shadowed-btn 上 ⇒ 判据可能坏了（反向断言缺失）"
  fi
fi

printf '\n[maestro-id] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
