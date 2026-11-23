#!/usr/bin/env bash
# maestro-readme-route-stats-gate.sh —— 守住 `maestro/README.md` 的路由口径清单不与当前树脱节（L10 复核轮）
#
# ## 为什么这道门存在
#
# ★ L10 首版把 README 的路由清单做成「**手工核对的产物**」⇒ **过期是静默的**：
#   2026-11-23 复核轮实测：上游 `41fb572f3`（团队层级管理前端切片落地）新增
#   `/admin/teams` 与 `/admin/teams/:id` ⇒ README §1.1 的 **129 / 134 / 22 / 117 / 54**
#   五个数**全部过期**，而**没有任何一处会红** ⇒ 「清单过期」只有等人想起来重跑才发现。
#
# ★ 同一轮还查出两个更早的静默缺陷：
#   ① §1.2「router.ts 行」列是**手抄**的 ⇒ **在任何提交上都对不上**；
#   ② §1.2 正文写「这 **19** 条」，与标题「**20** 条」自相矛盾。
#
# ⇒ 本门不是「检查 README 好不好看」——本门守的是：**★ 清单必须与当前树一致**。
#
# ## 判据
#   1.【行为】真跑 `maestro/tools/audit-readme-route-stats.py` ⇒ 必须绿
#      （7 条判据：D1/D2 算式自洽、D3/D4/D5 标题数 vs 表内行数、D6 幽灵路由、D7 未登记路由）。
#   2.【对照臂 A｜幽灵】README 副本里塞一条**树里不存在**的路由 ⇒ 必须红，且**红在 D6**。
#   3.【对照臂 B｜未登记】router.ts 副本里塞一条**README 没提**的新路由 ⇒ 必须红，且**红在 D7**。
#   4.【对照臂 C｜半回填】README 副本里把 §1.2 标题改成 19（表不动）⇒ 必须红，且**红在 D3**。
#      ★ 没有对照臂，门可能就是「恒绿」的 —— 工具写错了也照样过。
#
# 用法：bash scripts/checks/maestro-readme-route-stats-gate.sh
# ★ 运行时依赖：**只有 python3**（★ 不用 node —— CI 精简镜像里 node 不可靠，
#   仓内既有门 `scripts/checks/install-entrypoints-test.sh` 对 node 就是「有就跑、没有就 skip」
#   ⇒ 若本门以 node 为前提 ⇒ CI 上要么恒红、要么被 skip ⇒ **门等于没接**）。
#   ⇒ 对拍工具因此用 python3 写成（零第三方依赖）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOL="maestro/tools/audit-readme-route-stats.py"
PASS=0
FAIL=0

ok()  { PASS=$((PASS + 1)); printf '[maestro-readme-stats] ok: %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '[maestro-readme-stats] FAIL: %s\n' "$1" >&2; }

if ! command -v python3 >/dev/null 2>&1; then
  bad "python3 不在 PATH —— 对拍工具跑不了（工具不可运行 ⇒ 它的产出不可复现 ⇒ 门的价值归零）"
  printf '\n[maestro-readme-stats] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

if [[ ! -f "$TOOL" ]]; then
  bad "缺对拍工具 ${TOOL}"
  printf '\n[maestro-readme-stats] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

# ── 1. 行为判据：真跑工具，README 必须与当前树一致 ────────────────────────
tool_out="$(python3 "$TOOL" --json 2>&1)"
tool_rc=$?
if [[ $tool_rc -eq 0 ]]; then
  ok "对拍工具判定 README 路由口径清单与当前树一致"
else
  bad "对拍工具判定 README 路由口径清单已漂移（工具退出码 ${tool_rc}）——"
  printf '%s\n' "$tool_out" | sed 's/^/[maestro-readme-stats]   /' >&2
fi

# ★ 用临时目录做副本，**不改仓内文件**（门必须能在 CI 里跑 ⇒ 不能污染工作树）。
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/maestro-readme-stats.XXXXXX")"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

# ── 2. 对照臂 A：幽灵路由（README 登记了、树里没有）─────────────────────
cp maestro/README.md "$TMP_DIR/readme-ghost.md"
python3 - "$TMP_DIR/readme-ghost.md" <<'PY' 2>/dev/null || true
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
s = s.replace(
    '| `/admin/teams/:id` | `id` |',
    '| `/admin/ghost-route-not-in-router` | `id` |\n| `/admin/teams/:id` | `id` |',
    1,
)
open(p, 'w', encoding='utf-8').write(s)
PY

if ! grep -q 'admin/ghost-route-not-in-router' "$TMP_DIR/readme-ghost.md"; then
  bad "对照臂 A 没能注入幽灵路由（README 结构变了？）—— 对照臂本身失效，判据强度不可信"
else
  arm_out="$(python3 "$TOOL" --readme="$TMP_DIR/readme-ghost.md" --json 2>&1)"
  arm_rc=$?
  if [[ $arm_rc -eq 0 ]]; then
    bad "对照臂 A：幽灵路由存在时工具仍判绿 ⇒ 判据抓不住漂移（门恒绿）"
  else
    ok "对照臂 A：幽灵路由存在时工具判红（退出码 ${arm_rc}）"
    # 反向断言：必须红在「幽灵路由」这件事上，而不是随便红
    if printf '%s' "$arm_out" | grep -q 'admin/ghost-route-not-in-router'; then
      ok "对照臂 A：红在幽灵路由上（判据 D6 精确命中，不是随便红）"
    else
      bad "对照臂 A 虽然红了，但没红在幽灵路由上 ⇒ 判据可能坏了（反向断言缺失）"
    fi
  fi
fi

# ── 3. 对照臂 B：未登记路由（树里新增了、README 没提）────────────────────
cp web/src/router.ts "$TMP_DIR/router-unlisted.ts"
python3 - "$TMP_DIR/router-unlisted.ts" <<'PY' 2>/dev/null || true
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
s = s.replace(
    "  routes: [",
    "  routes: [\n    { path: '/admin/unlisted-route-for-gate-arm', component: TeamListView, meta: { requiresAuth: true } },",
    1,
)
open(p, 'w', encoding='utf-8').write(s)
PY

if ! grep -q 'unlisted-route-for-gate-arm' "$TMP_DIR/router-unlisted.ts"; then
  bad "对照臂 B 没能注入未登记路由（router.ts 结构变了？）—— 对照臂本身失效，判据强度不可信"
else
  arm_out="$(python3 "$TOOL" --router="$TMP_DIR/router-unlisted.ts" --json 2>&1)"
  arm_rc=$?
  if [[ $arm_rc -eq 0 ]]; then
    bad "对照臂 B：树里有 README 未登记的路由时工具仍判绿 ⇒ 判据抓不住漂移（门恒绿）"
  else
    ok "对照臂 B：树里有未登记路由时工具判红（退出码 ${arm_rc}）"
    if printf '%s' "$arm_out" | grep -q 'unlisted-route-for-gate-arm'; then
      ok "对照臂 B：红在未登记路由上（判据 D7 精确命中，不是随便红）"
    else
      bad "对照臂 B 虽然红了，但没红在未登记路由上 ⇒ 判据可能坏了（反向断言缺失）"
    fi
  fi
fi

# ── 4. 对照臂 C：半回填（只改表、忘了改标题）──────────────────────────────
cp maestro/README.md "$TMP_DIR/readme-half.md"
python3 - "$TMP_DIR/readme-half.md" <<'PY' 2>/dev/null || true
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
s = s.replace('### 1.2 纯 redirect 路由（20 条）', '### 1.2 纯 redirect 路由（19 条）', 1)
open(p, 'w', encoding='utf-8').write(s)
PY

if ! grep -q '纯 redirect 路由（19 条）' "$TMP_DIR/readme-half.md"; then
  bad "对照臂 C 没能注入半回填（README 结构变了？）—— 对照臂本身失效，判据强度不可信"
else
  arm_out="$(python3 "$TOOL" --readme="$TMP_DIR/readme-half.md" --json 2>&1)"
  arm_rc=$?
  if [[ $arm_rc -eq 0 ]]; then
    bad "对照臂 C：标题数与表内行数不一致时工具仍判绿 ⇒ 判据抓不住半回填（门恒绿）"
  else
    ok "对照臂 C：半回填时工具判红（退出码 ${arm_rc}）"
    if printf '%s' "$arm_out" | grep -q 'D3'; then
      ok "对照臂 C：红在 D3（标题条数 vs 表内行数）上（判据精确命中）"
    else
      bad "对照臂 C 虽然红了，但没红在 D3 上 ⇒ 判据可能坏了（反向断言缺失）"
    fi
  fi
fi

printf '\n[maestro-readme-stats] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
