#!/usr/bin/env bash
# scripts/checks/maestro-flow-anchor-gate.sh
#
# ★★ 「Maestro flow 的锚点必须在前端真实存在」门（L34 九轮 · 2026-11-24）★★
#
# ## 这道门为什么存在
#
# ★ 会话分析 E2E 有两半：
#   · **在线半**：真机跑 flow（要 node + 浏览器 + 在跑的网关）⇒ ★ 进不了 CI 载体；
#   · **离线半**：flow 与前端源码的**静态契约**⇒ ★ 只需要 python3 ⇒ **能进 CI 载体**。
# ★⇒ 本门守离线半，且它是 Goal 侧「会话分析 E2E 进 CI」目前**唯一能落地的那一半**。
#
# ★ 实测坐实的缺陷（L34 九轮）：
#   `maestro/flows/17-live-gateway-core-display.yaml` 断言 `id: "gateway-core-canvas"`
#   ⇒ ★★ 该锚点**在前端树上根本不存在**（真实锚点是 `gateway-core-box`）
#   ⇒ 且那条带 `optional: true` ⇒ ⇒ **断言恒真**：真机上它从来没验证过任何东西，
#      却让人以为「画布存在」这条已被 E2E 覆盖 ⇒ ★ **静默**（流程里表现为
#      「找不到元素」，极易被误判成「页面坏了」）。
# ★★ 而**全仓没有任何一道门会红**：全仓的 flow 锚点存在性判据**只以 vitest
#   钉测的形式存在两份**（`saTestIds.contract.spec.ts` /
#   `statsRangeTestIds.contract.spec.ts`）⇒ ★★ ① 它们**只覆盖 2 个 flow**；
#   ★★ ② 它们要 node ⇒ ★ **codeup 载体里根本没有 node** ⇒ ★★ ⇒ **在 CI 上等于零覆盖**。
# ★★ 而既有的 `audit-maestro-id-selectors.py` 其实**已经算出了**「前端找不到的 id」
#   （它叫 `unknown`）⇒ ★ 却**只打印不判红**，而且它自己的头注写着
#   「另有「flow↔testid」门负责」⇒ ★★ **那道门不存在**（本门补上）。
#
# ## 判据
#   1.【行为】真跑 `maestro/tools/audit-flow-anchor-existence.py` ⇒ 必须绿，
#      且必须扫到**非零**个锚点（扫空 ⇒ 门会恒绿）。
#   2.【对照臂 A · 真幽灵】临时目录里造一棵最小仓（flow 断言一个前端没有的锚点）
#      ⇒ 工具**必须红**且**红在该锚点上**（否则「随便红一下」也算过）。
#   3.【对照臂 B · 防假红】最小仓里放三种**真实**锚点（静态 `data-testid`、
#      动态模板串、HTML `id=`）⇒ 工具**必须绿**（★ 这一臂防的是「索引不全
#      ⇒ 把真锚点误判成幽灵」——★ 实测这类假阳性有 10 个：3 个 HTML `id=`
#      ＋ 7 个动态档位锚点）。
#   4.【对照臂 C · 原生 flow 排除】最小仓里放一个带 `appId:` 的原生 flow
#      （它的 id 是 Android resource-id，与前端无关）⇒ 工具**必须绿**
#      （防的是「排除机制其实没生效/或反之误把原生 flow 当幽灵」）。
#   5.【对照臂 D · 白名单棘轮】最小仓的白名单里写一条**其实能解析**的锚点
#      ⇒ 工具**必须红**（白名单是用来记「真的没法静态解析」的，不是用来消音的；
#      条目一旦过时 ⇒ 必须有人删掉它）。
#
# ★ 全部对照臂都在 `mktemp -d` 造的临时目录里 ⇒ **不改仓内任何文件**。
# ★ 运行时依赖：**只有 python3**（零第三方依赖）⇒ 可进 codeup 载体。
#
# 用法：bash scripts/checks/maestro-flow-anchor-gate.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOL="maestro/tools/audit-flow-anchor-existence.py"
PASS=0
FAIL=0

ok()  { PASS=$((PASS + 1)); printf '[flow-anchor] ok: %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '[flow-anchor] FAIL: %s\n' "$1" >&2; }

# ── 0. 工具必须可运行 ──────────────────────────────────────────────────
# ★ 工具跑不起来 ⇒ 它的产出不可复现 ⇒ 门的价值归零 ⇒ 判红（不 skip）。
if ! command -v python3 >/dev/null 2>&1; then
  bad "python3 不在 PATH —— 审计工具跑不了（工具不可运行 ⇒ 门的价值归零）"
  printf '\n[flow-anchor] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi
if [[ ! -f "$TOOL" ]]; then
  bad "缺审计工具 ${TOOL}"
  printf '\n[flow-anchor] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

# ── 1. 行为判据：真跑工具，仓内不得有解析不出来的锚点 ─────────────────
# ★ 取退出码**不走管道**（管道会吃掉退出码）⇒ 用命令替换直接拿 $?。
tool_out="$(python3 "$TOOL" --json 2>&1)"
tool_rc=$?
if [[ "$tool_rc" -eq 0 ]]; then
  ok "审计工具判定：仓内 flow 的每个 id 锚点都在前端树里存在"
else
  bad "审计工具判定：存在前端没有的锚点（⇒ 该断言在真机上永远找不到 ⇒ 恒真）——"
  printf '%s\n' "$tool_out" | sed 's/^/[flow-anchor]   /' >&2
fi

# 扫描量必须非零 ⇒ 否则「一个都不检查」会被读成「全过」。
anchor_count="$(printf '%s' "$tool_out" | sed -n 's/.*"anchors": \([0-9][0-9]*\).*/\1/p' | head -1)"
if [[ -z "${anchor_count}" ]]; then
  bad "读不出工具报告的锚点数量 ⇒ 报告形态变了或工具崩了（★ 判据量与量具形态不一致）"
elif [[ "$anchor_count" -lt 10 ]]; then
  bad "只扫到 ${anchor_count} 个锚点（< 10）⇒ 扫描面失效（★ 门会「一个都不检查」就判绿）"
else
  ok "扫描到 ${anchor_count} 个锚点（≥ 10 ⇒ 扫描面没空）"
fi

# ── 对照臂用的临时目录（★ 全部在临时目录里 ⇒ 不改仓内任何文件）─────────
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/maestro-flow-anchor.XXXXXX")"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

# 小工具：造一棵最小仓（★ 目录骨架必须是工具真正扫的那几个目录 ★）
make_repo() {
  local dir="$1"
  mkdir -p "$dir/maestro/flows" "$dir/web/src"
}

# ── 2. 对照臂 A：真幽灵 ⇒ 工具必须红，且红在那个锚点上 ────────────────
ARM_A="$TMP_DIR/arm-a"
make_repo "$ARM_A"
cat >"$ARM_A/maestro/flows/_probe.yaml" <<'YAML'
- assertVisible:
    id: "ghost-anchor"
YAML
cat >"$ARM_A/web/src/Probe.vue" <<'VUE'
<template>
  <button type="button" data-testid="real-button">ok</button>
</template>
VUE
arm_a_out="$(python3 "$TOOL" --root="$ARM_A" --min-flows=1 --min-anchors=1 2>&1)"
arm_a_rc=$?
if [[ "$arm_a_rc" -eq 0 ]]; then
  bad "对照臂 A：flow 断言了前端不存在的锚点，工具仍判绿 ⇒ 判据抓不住幽灵锚点（门恒绿）"
else
  ok "对照臂 A：幽灵锚点被判红（退出码 ${arm_a_rc}）"
  if printf '%s' "$arm_a_out" | grep -q 'ghost-anchor'; then
    ok "对照臂 A：红在那个幽灵锚点上（判据精确命中，不是随便红）"
  else
    bad "对照臂 A 虽然红了，但没红在 ghost-anchor 上 ⇒ 判据可能坏了（反向断言缺失）"
    printf '%s\n' "$arm_a_out" | sed 's/^/[flow-anchor]   /' >&2
  fi
fi

# ── 3. 对照臂 B：三种真实锚点 ⇒ 工具必须绿（防假红）────────────────────
ARM_B="$TMP_DIR/arm-b"
make_repo "$ARM_B"
cat >"$ARM_B/maestro/flows/_probe.yaml" <<'YAML'
- assertVisible:
    id: "static-anchor"
- assertVisible:
    id: "dynamic-anchor-today"
- assertVisible:
    id: "html-id-anchor"
YAML
cat >"$ARM_B/web/src/Probe.vue" <<'VUE'
<template>
  <button type="button" data-testid="static-anchor">a</button>
  <span :data-testid="`dynamic-anchor-${preset.id}`">b</span>
  <input id="html-id-anchor" />
</template>
VUE
arm_b_out="$(python3 "$TOOL" --root="$ARM_B" --min-flows=1 --min-anchors=1 2>&1)"
arm_b_rc=$?
if [[ "$arm_b_rc" -eq 0 ]]; then
  ok "对照臂 B：静态 data-testid / 动态模板串 / HTML id 三种真实锚点都判绿（无假红）"
else
  bad "对照臂 B：三种真实锚点被判红 ⇒ 索引不全 ⇒ 门会假红（★ 实测这类假阳性有 10 个）——"
  printf '%s\n' "$arm_b_out" | sed 's/^/[flow-anchor]   /' >&2
fi

# ── 4. 对照臂 C：原生 Android flow 的 id 不该被判幽灵 ─────────────────
ARM_C="$TMP_DIR/arm-c"
make_repo "$ARM_C"
cat >"$ARM_C/maestro/flows/_probe.yaml" <<'YAML'
appId: com.example.native
---
- assertVisible:
    id: "native-only-anchor"
YAML
cat >"$ARM_C/maestro/flows/_probe-web.yaml" <<'YAML'
- assertVisible:
    id: "static-anchor"
YAML
cat >"$ARM_C/web/src/Probe.vue" <<'VUE'
<template>
  <button type="button" data-testid="static-anchor">a</button>
</template>
VUE
arm_c_out="$(python3 "$TOOL" --root="$ARM_C" --min-flows=1 --min-anchors=1 2>&1)"
arm_c_rc=$?
if [[ "$arm_c_rc" -eq 0 ]]; then
  ok "对照臂 C：原生 Android flow（appId:）的 id 不被判幽灵（排除机制生效）"
else
  bad "对照臂 C：原生 flow 的 id 被判成幽灵 ⇒ 排除机制没生效 ⇒ 门会假红——"
  printf '%s\n' "$arm_c_out" | sed 's/^/[flow-anchor]   /' >&2
fi

# ── 5. 对照臂 D：白名单棘轮（过时条目必须判红）────────────────────────
ARM_D="$TMP_DIR/arm-d"
make_repo "$ARM_D"
cat >"$ARM_D/maestro/flows/_probe.yaml" <<'YAML'
- assertVisible:
    id: "static-anchor"
YAML
cat >"$ARM_D/web/src/Probe.vue" <<'VUE'
<template>
  <button type="button" data-testid="static-anchor">a</button>
</template>
VUE
cat >"$ARM_D/maestro/flow-anchor-allowlist.tsv" <<'TSV'
maestro/flows/_probe.yaml	static-anchor	this anchor does resolve, so the entry is stale
TSV
arm_d_out="$(python3 "$TOOL" --root="$ARM_D" --min-flows=1 --min-anchors=1 2>&1)"
arm_d_rc=$?
if [[ "$arm_d_rc" -eq 0 ]]; then
  bad "对照臂 D：白名单里一条**能解析**的锚点被判绿 ⇒ 白名单会变成消音器（棘轮失效）"
else
  ok "对照臂 D：过时白名单条目被判红（退出码 ${arm_d_rc} ⇒ 白名单只许收窄）"
fi

printf '\n[flow-anchor] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
