#!/usr/bin/env bash
# scripts/checks/maestro-users-role-gate.sh
#
# ★★ 「用户管理页角色治理」真机资产的**离线契约门**（2026-11-26 L135 补真机覆盖轮）★★
#
# ## 这道门为什么存在
#
# ★ 上一轮的角色治理功能**已上线且已验收**，但本轮补真机覆盖时撞出一个**会静默毁掉断言**的坑：
#   · 租户详情页用 element-plus 的 `<el-select>` ⇒ 它的 option **只在展开时渲染**
#     ⇒ 未展开时页面 DOM 里没有「超级管理员」四个字
#     ⇒ 「下拉里没有超级管理员」可以用 `assertNotVisible` **干净地**断言；
#   · `/users` 页用**原生 `<select>`** ⇒ 它的 option **始终在 DOM 里**
#     ⇒ 「超级管理员」四个字即使不展开也**一直在可访问树里**
#     ⇒ ★★ 在 `/users` 页用 `assertNotVisible: ".*超级管理员.*"` 断言「下拉里没有超级管理员」
#       **是恒红的假门**（必然被筛选区那个角色下拉的 option 命中）。
# ★⇒ 而 flow 20（租户详情页）里**恰好就有**那条 `assertNotVisible` ⇒ ★ **照搬就会写出一条假门**。
#
# ★★ 所以本门守的是那条**容易被照搬坏的契约**：
#   ① 三个资产必须都在（探针 / 自检 / flow 21）；
#   ② ★★ **flow 21 里不得出现「assertNotVisible ＋ 超级管理员」**（那是在原生 select 页上的假门）；
#   ③ 探针必须登记 `--negate` 自证开关、且**取反点 ≥ 4 处**（否则自证覆盖不全）；
#   ④ 探针必须把判定口径交给 `result.mjs`（仓内唯一真源）。
#
# ## 为什么判据用 python3 而不是 node
# ★ 与 `scripts/checks/maestro-flow-anchor-gate.sh` 头注同源的一条硬事实：
#   **codeup 载体里没有 node** ⇒ ★ **凡是依赖 node 的判据在 CI 上等于零覆盖**。
#   ⇒ 本门的**必跑判据全部用 python3**（零第三方依赖）⇒ 能进 CI 载体；
#   ★ node 存在时**追加**跑 `assert-users-role-options.selftest.mjs`（增强层、不是必跑层）——
#     ★ 它钉的是「自证开关本身是否生效」，属于**执行期**判据，python3 静态扫描做不了。
#
# ## 对照臂（★ 证明这些钉不是恒绿的 ★）
#   A. 造一份**加了** `assertNotVisible`「超级管理员」的 flow 副本 ⇒ 检查**必须红**；
#   B. 造一份**删掉了** `--negate` 登记的探针副本 ⇒ 检查**必须红**；
#   C. 删掉 flow 21 本体 ⇒ 检查**必须红**（资产存在性不是恒真）。
# ★ 全部对照臂都在 `mktemp -d` 造的临时目录里 ⇒ **不改仓内任何文件**。
#
# 用法：bash scripts/checks/maestro-users-role-gate.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

PASS=0
FAIL=0
ok()  { printf '[users-role] ok: %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '[users-role] FAIL: %s\n' "$1"; FAIL=$((FAIL+1)); }

PROBE="maestro/tools/assert-users-role-options.mjs"
SELFTEST="maestro/tools/assert-users-role-options.selftest.mjs"
FLOW="maestro/flows/21-users-page-role-governance.yaml"

# ── 扫描器：★ 判据本体在 python3 里（★ 静态契约，★ 与「被扫文件」解耦 ⇒ 可用副本做对照臂）──
# 用法：scan.py <root> <probe_rel> <selftest_rel> <flow_rel>
scan() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import json, os, re, sys

root, probe_rel, selftest_rel, flow_rel = sys.argv[1:5]
out = {"fail": [], "ok": [], "counts": {}}

def read(rel):
    p = os.path.join(root, rel)
    if not os.path.isfile(p):
        return None
    with open(p, encoding='utf-8', errors='replace') as f:
        return f.read()

probe = read(probe_rel)
selftest = read(selftest_rel)
flow = read(flow_rel)

# ── ① 资产存在性 ────────────────────────────────────────────────────────
missing = [r for r, c in ((probe_rel, probe), (selftest_rel, selftest), (flow_rel, flow)) if c is None]
if missing:
    out["fail"].append("资产缺失：" + ", ".join(missing))
else:
    out["ok"].append("三个资产都在")

if probe is not None:
    # ── ② flow 21 不得含「assertNotVisible ＋ 超级管理员」（★ 原生 select 页上的恒红假门）──
    # ★ flow 缺失时**必须跳过本项**（而不是去 split 一个 None ⇒ 判据崩了
    #   ⇒ 「崩」会被下游当成「没测到」⇒ 对照臂 C 就测不出「资产缺失报红」这件事）。
    if flow is None:
        out["ok"].append("flow 缺失 ⇒ 跳过假门扫描（缺失已记为 fail）")
    else:
        bad_lines = []
        for i, line in enumerate(flow.split("\n"), 1):
            s = line.strip()
            if s.startswith("#"):
                continue  # 注释里说明「为什么不能写」是允许的、且是必要的
            if "assertNotVisible" in s and "超级管理员" in s:
                bad_lines.append(i)
        if bad_lines:
            out["fail"].append(
                "flow 里出现了 assertNotVisible ＋ 超级管理员（第 %s 行）⇒ 原生 <select> 的 option 常驻 DOM ⇒ 那条断言恒红"
                % ",".join(map(str, bad_lines))
            )
        else:
            out["ok"].append("flow 无「assertNotVisible ＋ 超级管理员」的假门")

    # ── ③ 探针必须登记 --negate 自证开关、且取反点 ≥ 4 ────────────────
    if "--negate" not in probe:
        out["fail"].append("探针没有登记 --negate 自证开关 ⇒ 判据无法自证")
    else:
        out["ok"].append("探针登记了 --negate 自证开关")
    neg_uses = len(re.findall(r"NEGATE\s*\?", probe))
    out["counts"]["negate_uses"] = neg_uses
    if neg_uses < 4:
        out["fail"].append("探针的取反点只有 %d 处（< 4）⇒ 自证覆盖不全" % neg_uses)
    else:
        out["ok"].append("探针取反点 %d 处（≥ 4）" % neg_uses)

    # ── ④ 判定口径必须交给 result.mjs（仓内唯一真源）──────────────────
    if not re.search(r"import\s*\{[^}]*summarize[^}]*\}\s*from\s*'\./result\.mjs'", probe):
        out["fail"].append("探针没有 import result.mjs 的 summarize ⇒ 判定口径会与仓内其它工具漂移")
    else:
        out["ok"].append("探针的判定口径走 result.mjs")

if selftest is not None:
    # ── ⑤ 自检必须带变异反向臂（★ 钉本身不能恒绿）────────────────────
    arms = len(re.findall(r"变异臂", selftest))
    out["counts"]["mut_arms"] = arms
    if arms < 2:
        out["fail"].append("自检的变异反向臂只有 %d 条（< 2）⇒ 钉可能是恒绿的" % arms)
    else:
        out["ok"].append("自检带 %d 条变异反向臂" % arms)

print(json.dumps(out, ensure_ascii=False))
PY
}

# ── 1. 行为臂：真仓扫描必须绿 ─────────────────────────────────────────────
RES="$(scan "$ROOT" "$PROBE" "$SELFTEST" "$FLOW")"
if [ -z "$RES" ]; then
  bad "扫描器自身没输出（python3 不可用？）⇒ 判据未评估"
else
  # 逐条打印（用 python 解析，避免 shell 里处理中文 JSON）
  printf '%s' "$RES" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for m in d["ok"]: print("OK\t"+m)
for m in d["fail"]: print("FAIL\t"+m)
' | while IFS=$'\t' read -r kind msg; do
    if [ "$kind" = "OK" ]; then ok "$msg"; else bad "$msg"; fi
  done
  # ★ while 在子 shell 里 ⇒ PASS/FAIL 不会回传 ⇒ 这里用计数兜底
  RES_FAIL="$(printf '%s' "$RES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["fail"]))')"
  RES_OK="$(printf '%s' "$RES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["ok"]))')"
  PASS=$((PASS + RES_OK))
  FAIL=$((FAIL + RES_FAIL))
fi

# ── 2. 对照臂 A：加了「assertNotVisible ＋ 超级管理员」的 flow 副本 ⇒ 必须红 ──
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/maestro/flows" "$TMP/maestro/tools"
cp "$FLOW" "$TMP/$FLOW"
cp "$PROBE" "$TMP/$PROBE"
cp "$SELFTEST" "$TMP/$SELFTEST"
printf '\n- assertNotVisible: ".*超级管理员.*"\n' >>"$TMP/$FLOW"
A_RES="$(scan "$TMP" "$PROBE" "$SELFTEST" "$FLOW")"
A_FAIL="$(printf '%s' "$A_RES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["fail"]))' 2>/dev/null || echo 0)"
if [ "${A_FAIL:-0}" -ge 1 ]; then
  ok "对照臂 A：加了假门的 flow 副本被判红（fail=${A_FAIL}）"
else
  bad "对照臂 A：加了假门的 flow 副本仍判绿 ⇒ 门恒绿（判据抓不住恒红假门）"
fi

# ── 3. 对照臂 B：删掉 --negate 登记的探针副本 ⇒ 必须红 ─────────────────────
# ★ 替换串必须**不含** `--negate` 子串（★ 写成 `--negate-X` 的话检查项
#   `"--negate" not in probe` 仍为假 ⇒ 对照臂自己就假绿了 ⇒ 判据抓不到东西）。
sed 's/--negate/NEGFLAG/g' "$PROBE" >"$TMP/$PROBE"
B_RES="$(scan "$TMP" "$PROBE" "$SELFTEST" "$FLOW")"
B_FAIL="$(printf '%s' "$B_RES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["fail"]))' 2>/dev/null || echo 0)"
if [ "${B_FAIL:-0}" -ge 1 ]; then
  ok "对照臂 B：删掉 --negate 登记的探针副本被判红（fail=${B_FAIL}）"
else
  bad "对照臂 B：删掉 --negate 登记的探针副本仍判绿 ⇒ 门恒绿"
fi

# ── 4. 对照臂 C：flow 本体缺失 ⇒ 必须红 ───────────────────────────────────
rm -f "$TMP/$FLOW"
C_RES="$(scan "$TMP" "$PROBE" "$SELFTEST" "$FLOW")"
C_FAIL="$(printf '%s' "$C_RES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["fail"]))' 2>/dev/null || echo 0)"
if [ "${C_FAIL:-0}" -ge 1 ]; then
  ok "对照臂 C：flow 本体缺失被判红（fail=${C_FAIL}）"
else
  bad "对照臂 C：flow 本体缺失仍判绿 ⇒ 资产存在性判据恒真"
fi

# ── 5. 增强层（★ 非必跑）：node 在 ⇒ 追加跑自检的执行期判据 ────────────────
if command -v node >/dev/null 2>&1; then
  if node "$SELFTEST" >"$TMP/selftest.log" 2>&1; then
    ok "增强层：selftest 执行期判据全绿（node $(node --version)）"
  else
    bad "增强层：selftest 判红（★ 详见下方输出）"
    tail -20 "$TMP/selftest.log" | sed 's/^/[users-role]   /'
  fi
else
  printf '[users-role] skip: 增强层 selftest（本机无 node ⇒ ★ 静态判据已覆盖能进 CI 的那部分）\n'
fi

printf '\n[users-role] pass=%s fail=%s\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
