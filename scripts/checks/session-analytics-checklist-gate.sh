#!/usr/bin/env bash
# session-analytics-checklist-gate.sh —— 守住 PR 模板「页面改动 Checklist」与当前树一致（L12 补轮）
#
# ## 为什么这道门存在
#
# L12 的原始诉求：「会话分析 E2E 没接入 CI ⇒ 改了页面但没跑 E2E 不会被拦住」。
# 上游 2026-11-20 的收口（`035b0c4b7`）走 PR checklist 方案：在
# `.github/pull_request_template.md` 里列出「改了这些页面就必须复跑会话分析 E2E」。
#
# ★ 但那份清单是**手写的**，且在 2026-11-23 **实测漂移了两处**：
#   ① **幽灵条目**：模板列的 `SessionAnalyticsOverviewView.vue` —— 上游把
#      「会话分析总览页」取消了（路由改 redirect 到 users 页）⇒ **文件已不存在**。
#   ② **漏项**：上游把总览页内容**重组迁入** `SessionAnalyticsUsersView.vue`
#      ⇒ ★ **这个新页面不在清单里** ⇒ ★ **改了最需要复跑 E2E 的页面，
#      checklist 反而不会要求复跑** ⇒ **L12 的保护恰好在最需要处失效**。
#
# ⇒ 本门不是「检查 PR 有没有勾选」——那需要 PR body 且无判据强度；
#   本门守的是更前面、更容易静默失效的一环：**★ 清单本身必须与当前树一致**。
#   （勾选是否勾了，仍由 reviewer 负责；清单对了，勾选才有意义。）
#
# ## 判据
#   1.【行为】真跑 `maestro/tools/audit-page-change-checklist.py` ⇒ 必须绿。
#      该工具三条判据：A 幽灵文件 / B 漏项文件 / C 幽灵路径。
#   2.【对照臂】把模板换成**故意漂移的副本**再跑 ⇒ **必须红**。
#      ★ 没有这一条，门可能就是「恒绿」的 —— 工具写错了也照样过。
#   3.【反向断言】对照臂必须**精确红在判据上**（A/B/C 任一命中即可），
#      而不是「随便红一下」—— 否则工具坏了也会被当成「对照臂生效」。
#
# 用法：bash scripts/checks/session-analytics-checklist-gate.sh
# ★ 运行时依赖：**只有 python3**（★ 不用 node —— CI 精简镜像里 node 不可靠，
#   仓内既有门 `scripts/checks/install-entrypoints-test.sh` 对 node 就是「有就跑、没有就 skip」
#   ⇒ 若本门以 node 为前提 ⇒ CI 上要么恒红、要么被 skip ⇒ **门等于没接**）。
#   ⇒ 对拍工具因此用 python3 写成（`maestro/tools/audit-page-change-checklist.py`，零第三方依赖）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOL="maestro/tools/audit-page-change-checklist.py"
PASS=0
FAIL=0

ok()  { PASS=$((PASS + 1)); printf '[sa-checklist] ok: %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf '[sa-checklist] FAIL: %s\n' "$1" >&2; }

if ! command -v python3 >/dev/null 2>&1; then
  bad "python3 不在 PATH —— 对拍工具跑不了（工具不可运行 ⇒ 它的产出不可复现 ⇒ 门的价值归零）"
  printf '\n[sa-checklist] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

if [[ ! -f "$TOOL" ]]; then
  bad "缺对拍工具 ${TOOL}"
  printf '\n[sa-checklist] pass=%s fail=%s\n' "$PASS" "$FAIL"
  exit 1
fi

# ── 1. 行为判据：真跑工具，模板必须与当前树一致 ────────────────────────
tool_out="$(python3 "$TOOL" --json 2>&1)"
tool_rc=$?
if [[ $tool_rc -eq 0 ]]; then
  ok "对拍工具判定模板清单与当前树一致"
else
  bad "对拍工具判定模板清单已漂移（工具退出码 ${tool_rc}）——"
  printf '%s\n' "$tool_out" | sed 's/^/[sa-checklist]   /' >&2
fi

# ── 2/3. 对照臂：故意漂移的模板副本必须红，且必须红在判据上 ─────────────
# ★ 用临时目录做副本，**不改仓内文件**（门必须能在 CI 里跑 ⇒ 不能污染工作树）。
TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t sa-checklist)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

TEMPLATE_REL=".github/pull_request_template.md"
cp "$TEMPLATE_REL" "$TMP_DIR/tpl-ghost.md"
# 注入一个**仓内不存在**的会话分析视图名 ⇒ 判据 A 必须命中
python3 - "$TMP_DIR/tpl-ghost.md" <<'PY' 2>/dev/null || true
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
s = s.replace(
    '- [ ] `/admin/session-analytics/users`',
    '- [ ] `/admin/session-analytics/ghost`（幽灵页：`SessionAnalyticsGhostView.vue`）\n- [ ] `/admin/session-analytics/users`',
    1,
)
open(p, 'w', encoding='utf-8').write(s)
PY

if ! grep -q 'SessionAnalyticsGhostView.vue' "$TMP_DIR/tpl-ghost.md"; then
  bad "对照臂没能注入幽灵条目（模板结构变了？）—— 对照臂本身失效，判据强度不可信"
else
  arm_out="$(python3 "$TOOL" --template="$TMP_DIR/tpl-ghost.md" --json 2>&1)"
  arm_rc=$?
  if [[ $arm_rc -eq 0 ]]; then
    bad "对照臂：幽灵条目存在时工具仍判绿 ⇒ 判据抓不住漂移（门恒绿）"
  else
    ok "对照臂：幽灵条目存在时工具判红（退出码 ${arm_rc}）"
    # 反向断言：必须红在「幽灵」这件事上，而不是随便红
    if printf '%s' "$arm_out" | grep -q 'SessionAnalyticsGhostView.vue'; then
      ok "对照臂：红在幽灵条目上（判据 A 精确命中，不是随便红）"
    else
      bad "对照臂虽然红了，但没红在幽灵条目上 ⇒ 判据可能坏了（反向断言缺失）"
    fi
  fi
fi

printf '\n[sa-checklist] pass=%s fail=%s\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
