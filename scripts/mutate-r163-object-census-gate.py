#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R163 轮的变异验证脚本（★ `ON ONLY` / 列级联退役 / 新装可达性）。

# 这道脚本在钉什么

★ 仓内有一条纪律：**「证伪一道『保证』的唯一办法是【真的把它打破一次】」**。
★ R163 轮改了 R161/R162 轮交付的对账器（★ 期望集推导、状态分类、可达性判定），
★ 所以本脚本把**每一条新判据**都真的打破一次，★ 确认对应的门会红。

★ ★★ 三条纪律（★ 同族脚本的既有约定）：

① ★ **「红了」≠「验到了你想验的那条腿」** ⇒ 每条变异都**指名**它必须红在哪条腿上
   （★ 上游把那条腿改名/删除，★ 剩余腿照样能红 ⇒ 脚本照样报「验证通过」）。
② ★ **编译红不算数**（★ 只说明代码坏了，★ 不说明判据有牙）⇒ 只认**断言红**。
③ ★ **还原必须一字不差** ⇒ 还原后**逐字 md5 比对**。

★ ★★ 本轮特别要验的一条（R162 教训的第二次实证）：

★★ **「两份独立实现互证」也可能同源同错** ⇒ ★★ `ON ONLY` 这个缺陷在 Python 工具与
★★ Go 门里**一模一样** ⇒ ⇒ 所以本脚本要验的是：**两侧【各修一次】之后，★ 互证真的有牙**
★★ ⇒ ★★ 即：★ 单侧退回缺陷 ⇒ ★★ 互证必须立刻红。

# 用法

    python3 scripts/mutate-r163-object-census-gate.py

# ★ 残留声明（★ `scripts/audit/mutation_residue_guard.py` 会按它检查残留）

    TARGET = REPO / "scripts/audit/db-object-census.py"
    TARGET = REPO / "scripts/ursmcheck/db_object_census_gate_test.go"
    TARGET = REPO / "scripts/ursmcheck/installer_reachability_gate_test.go"
    TARGET = REPO / "scripts/audit/asset/db-object-columns.tsv"
    TARGET = REPO / "scripts/audit/asset/installer-reachability.tsv"
    TARGET = REPO / "scripts/audit/asset/db-object-census.tsv"
"""

import hashlib
import os
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]

# ★ 残留门按这几个声明收集「本脚本会写的文件」
TARGET = REPO / "scripts/audit/db-object-census.py"
TARGET = REPO / "scripts/ursmcheck/db_object_census_gate_test.go"
TARGET = REPO / "scripts/ursmcheck/installer_reachability_gate_test.go"
TARGET = REPO / "scripts/audit/asset/db-object-columns.tsv"
TARGET = REPO / "scripts/audit/asset/installer-reachability.tsv"
TARGET = REPO / "scripts/audit/asset/db-object-census.tsv"

PY_TOOL_REL = "scripts/audit/db-object-census.py"
GO_GATE_REL = "scripts/ursmcheck/db_object_census_gate_test.go"
GO_REACH_REL = "scripts/ursmcheck/installer_reachability_gate_test.go"
COLUMNS_REL = "scripts/audit/asset/db-object-columns.tsv"
REACH_REL = "scripts/audit/asset/installer-reachability.tsv"
CENSUS_REL = "scripts/audit/asset/db-object-census.tsv"

GO_ENV = {
    "PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:" + str(pathlib.Path.home() / "sdk/go/bin"),
    "GOPROXY": "https://goproxy.cn,direct",
    "GOMODCACHE": str(pathlib.Path.home() / "sdk/gomodcache"),
    "HOME": str(pathlib.Path.home()),
}

# ★★ 单侧退回「ON ONLY」缺陷的锚点（★ 两侧各一个 ⇒ ⇒ 互证必须能抓住）
PY_ONLY_ANCHOR = """RE_CREATE_INDEX = re.compile(
    r"CREATE\\s+(?:UNIQUE\\s+)?INDEX\\s+(?:CONCURRENTLY\\s+)?(?:IF\\s+NOT\\s+EXISTS\\s+)?"
    r'([a-z0-9_"]+)\\s+ON\\s+(?:ONLY\\s+)?([a-z0-9_."]+)',
    re.I,
)"""
PY_ONLY_BROKEN = """RE_CREATE_INDEX = re.compile(
    r"CREATE\\s+(?:UNIQUE\\s+)?INDEX\\s+(?:CONCURRENTLY\\s+)?(?:IF\\s+NOT\\s+EXISTS\\s+)?"
    r'([a-z0-9_"]+)\\s+ON\\s+([a-z0-9_."]+)',
    re.I,
)"""

# ★★ 注意：★ Go 侧有两处正则，★ 两处都必须「活」：
# ★★   ① dbObjReCreateHead —— ★【括号配对扫描】用（★ 真正驱动期望集推导）
# ★★   ② dbObjReCreate    —— ★【老正则】（★ 与 ① 互为交叉校验，★ 防任一侧单独漂移）
# ★★ ⇒ ★★ 单侧退回缺陷 ⇒ ★★ 互证必须立刻红 ⇒ ★★ 这才是 R162 教训的正确用法。
GO_ONLY_HEAD_ANCHOR = '''	dbObjReCreateHead = regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)\s+ON\s+(?:ONLY\s+)?([a-z0-9_."]+)\s*(?:USING\s+[a-z0-9_]+\s*)?\(`)'''
GO_ONLY_HEAD_BROKEN = '''	dbObjReCreateHead = regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)\s+ON\s+([a-z0-9_."]+)\s*(?:USING\s+[a-z0-9_]+\s*)?\(`)'''
GO_ONLY_ANCHOR = '''	dbObjReCreate = regexp.MustCompile(`(?is)CREATE\\s+(?:UNIQUE\\s+)?INDEX\\s+(?:CONCURRENTLY\\s+)?(?:IF\\s+NOT\\s+EXISTS\\s+)?([a-z0-9_"]+)\\s+ON\\s+(?:ONLY\\s+)?([a-z0-9_."]+)`)'''
GO_ONLY_BROKEN = '''	dbObjReCreate = regexp.MustCompile(`(?is)CREATE\\s+(?:UNIQUE\\s+)?INDEX\\s+(?:CONCURRENTLY\\s+)?(?:IF\\s+NOT\\s+EXISTS\\s+)?([a-z0-9_"]+)\\s+ON\\s+([a-z0-9_."]+)`)'''

PY_CASCADE_ANCHOR = """            if [c for c in keycols if c not in have]:
                return "retired-by-column-drop\""""
PY_CASCADE_BROKEN = """            if [c for c in keycols if c not in have]:
                return "missing\""""

GO_CASCADE_ANCHOR = """			for _, c := range keycols {
				if !have[c] {
					return "retired-by-column-drop"
				}
			}"""
GO_CASCADE_BROKEN = """			for _, c := range keycols {
				if !have[c] {
					return "missing"
				}
			}"""


def md5(path: pathlib.Path) -> str:
    return hashlib.md5(path.read_bytes()).hexdigest()


def run(cmd, cwd=REPO):
    env = {**dict(os.environ), **GO_ENV}
    p = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True, env=env)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


# ─────────────────────────── 变异定义 ───────────────────────────
# (名称, 相对路径, 旧文本, 新文本, 期望红的那条腿, 怎么判红)
MUTATIONS = [
    (
        "M1-PY-ONLY-BUG",
        PY_TOOL_REL,
        PY_ONLY_ANCHOR,
        PY_ONLY_BROKEN,
        "python-selftest:expect/no-only-as-table-name",
        "selftest",
    ),
    (
        "M2-GO-ONLY-HEAD-BUG",
        GO_GATE_REL,
        GO_ONLY_HEAD_ANCHOR,
        GO_ONLY_HEAD_BROKEN,
        "go:TestDbObjectExpectSetMatchesIndependentDerivation",
        "go",
    ),
    (
        "M2B-GO-ONLY-REGEX-BUG",
        GO_GATE_REL,
        GO_ONLY_ANCHOR,
        GO_ONLY_BROKEN,
        "go:TestDbObjectExpectSetMatchesIndependentDerivation",
        "go",
    ),
    (
        "M3-PY-NO-CASCADE",
        PY_TOOL_REL,
        PY_CASCADE_ANCHOR,
        PY_CASCADE_BROKEN,
        "python-selftest:classify/retired-by-column-drop",
        "selftest",
    ),
    (
        "M4-GO-NO-CASCADE",
        GO_GATE_REL,
        GO_CASCADE_ANCHOR,
        GO_CASCADE_BROKEN,
        "go:TestDbObjectGapLedgerIsFullyRecomputable",
        "go",
    ),
    (
        "M5-COLUMNS-DROP-COLUMN",
        COLUMNS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectGapLedgerIsFullyRecomputable",
        "go",
    ),
    (
        "M6-REACH-CREATOR-TAMPER",
        REACH_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestInstallerReachabilityLedgerIsRecomputable",
        "go",
    ),
    (
        "M7-REACH-VERDICT-TAMPER",
        REACH_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestInstallerReachabilityLedgerIsRecomputable",
        "go",
    ),
    (
        "M8-REACH-INSTALLER-BUG-INJECT",
        REACH_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestInstallerReachabilityRatchet",
        "go",
    ),
    (
        "M9-REACH-EMPTY",
        REACH_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestInstallerReachabilityLedgerIsRecomputable",
        "go",
    ),
    (
        "M10-CONTROL-NOTE-COLUMN",
        REACH_REL,
        None,  # ★ 特殊处理：★ 改【note 自由文本列】⇒ ★ 判定结果不变
        None,
        "green",
        "go",
    ),
    (
        "M11-CONTROL-COMMENT-ONLY",
        PY_TOOL_REL,
        "def _bare_name(raw: str) -> str:",
        "def _bare_name(raw: str) -> str:\n    # ★ 对照变异：★ 只加注释 ⇒ ★ 行为不变 ⇒ ★ 门必须仍绿",
        "green",
        "selftest+go",
    ),
]


def apply_mutation(name, rel, old, new):
    """★ 施加变异 ⇒ 返回 (原字节, 是否成功)。"""
    path = REPO / rel
    original = path.read_bytes()
    if name == "M5-COLUMNS-DROP-COLUMN":
        # ★★★ 往列底账里【加】一行「local 的 sessions.last_full_payload_at」
        # ⇒ ⇒ ★★ local 的 idx_sessions_last_full_at 会从 retired-by-column-drop 掉回 missing
        # ⇒ ⇒ ⇒ 而 211 是【唯一】那列还在的环境（★ 顺序倒置的指纹）
        # ⇒ ⇒ ⇒ 门必须红（★ 证明「列底账」确实是判定的真输入，★ 且顺序倒置能被检出）
        # ★★ ★★ 方向很关键：★ 【删】一行不改变判定（★ 「键列不在 ⇒ 退役」本来就是删除方向）
        # ★★ ★★ ⇒ ⇒ 所以这条变异必须【加】列，★ 才能改变判定
        lines = original.decode("utf-8").split("\n")
        out = []
        hit = False
        for line in lines:
            out.append(line)
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) == 4 and cols[0] == "local" and cols[2] == "sessions" \
                    and cols[3] == "tenant_id":
                out.append("local\tpublic\tsessions\tlast_full_payload_at")
                hit = True
        if not hit:
            return original, False
        path.write_bytes("\n".join(out).encode("utf-8"))
        return original, True
    if name in ("M6-REACH-CREATOR-TAMPER", "M7-REACH-VERDICT-TAMPER",
                "M8-REACH-INSTALLER-BUG-INJECT", "M9-REACH-EMPTY"):
        lines = original.decode("utf-8").split("\n")
        if name == "M9-REACH-EMPTY":
            kept = [l for l in lines if l.startswith("#") or not l.strip()]
            path.write_bytes(("\n".join(kept) + "\n").encode("utf-8"))
            return original, True
        hit = False
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) != 10:
                continue
            if name == "M6-REACH-CREATOR-TAMPER" and cols[4] == "yes":
                cols[4] = "no"
                hit = True
            elif name == "M7-REACH-VERDICT-TAMPER" and cols[7] == "reachable-and-present":
                cols[7] = "baseline-dependent"
                hit = True
            elif name == "M8-REACH-INSTALLER-BUG-INJECT" and cols[8] == "none":
                cols[8] = "installer-bug"
                hit = True
            if hit:
                lines[i] = "\t".join(cols)
                break
        if not hit:
            return original, False
        path.write_bytes("\n".join(lines).encode("utf-8"))
        return original, True
    if name == "M10-CONTROL-NOTE-COLUMN":
        # ★ 改可达性台账的【note 自由文本列】⇒ ★ 没有任何判据读它 ⇒ ★ 门应仍绿
        lines = original.decode("utf-8").split("\n")
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) == 10 and cols[9] == "":
                cols[9] = "mutated_note_only"
                lines[i] = "\t".join(cols)
                path.write_bytes("\n".join(lines).encode("utf-8"))
                return original, True
        return original, False
    text = original.decode("utf-8")
    if text.count(old) != 1:
        sys.stderr.write("★ 锚点在 %s 里命中 %d 次（★ 必须恰好 1 次）\n" % (rel, text.count(old)))
        return original, False
    path.write_text(text.replace(old, new, 1), encoding="utf-8")
    return original, True


def restore(rel, original):
    (REPO / rel).write_bytes(original)


def check_leg(kind, leg, name):
    """★ 判「红」必须红在**指名的那条腿**上。"""
    if kind == "selftest":
        rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
        if leg.startswith("python-selftest:"):
            needle = leg.split(":", 1)[1]
            return rc != 0 and ("FAIL %s" % needle) in out, out
        return False, out
    if kind == "go" and leg == "green":
        rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject|TestInstallerReachability|TestUnreachable|TestBaseline", "-count=1", "-v"])
        return rc == 0 and ("--- PASS: TestDbObjectExpectSetMatchesIndependentDerivation" in out), out
    if kind == "go":
        # ★ 腿名带 "go:" 前缀只是给人看的（★ 脚本自述的腿名），★ -run 只认函数名
        fn = leg.split(":", 1)[1] if leg.startswith("go:") else leg
        rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", fn, "-count=1", "-v"])
        return rc != 0 and ("--- FAIL: %s" % fn) in out, out
    if kind == "selftest+go":
        rc1, out1 = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
        rc2, out2 = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject|TestInstallerReachability|TestUnreachable|TestBaseline", "-count=1"])
        return rc1 == 0 and rc2 == 0, out1 + out2
    return False, ""


def main():
    # ★ 基线必须绿（★ 否则「变异后红」毫无意义）
    rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
    if rc != 0:
        sys.stderr.write("★ 基线 selftest 不绿 ⇒ 停\n" + out[-2000:])
        return 1
    rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject|TestInstallerReachability|TestUnreachable|TestBaseline", "-count=1"])
    if rc != 0:
        sys.stderr.write("★ 基线 go 门不绿 ⇒ 停\n" + out[-2000:])
        return 1
    print("★ 基线绿（★ selftest + go 门）")

    results = []
    for name, rel, old, new, leg, kind in MUTATIONS:
        original, ok = apply_mutation(name, rel, old, new)
        if not ok:
            results.append((name, False, "★ 变异未施加"))
            print("%-34s %-6s ✘  ★ 变异未施加" % (name, "GREEN" if leg == "green" else "RED"))
            continue
        try:
            red, out = check_leg(kind, leg, name)
            detail = ""
            if not red:
                detail = "★ 该红的地方没红（★ 期望红在 %s）" % leg
            results.append((name, red, detail))
            want = "GREEN" if leg == "green" else "RED"
            print("%-34s %-6s %s  %s" % (name, want, "✔" if red else "✘", detail))
            if not red:
                print(out[-1500:])
        finally:
            restore(rel, original)
            # ★ 还原必须一字不差
            after = md5(REPO / rel)
            before = hashlib.md5(original).hexdigest()
            if after != before:
                results.append((name, False, "★ 还原后 md5 不一致 ⇒ 残留！"))
                print("★ ★★ 还原失败：%s（★ 残留！）" % rel)

    print("\n★ 变异结果：")
    for name, ok, detail in results:
        print("  %-34s %s  %s" % (name, "PASS" if ok else "FAIL", detail))

    # ★ 还原后复跑基线（★ 证明没有残留）
    rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
    rc2, out2 = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject|TestInstallerReachability|TestUnreachable|TestBaseline", "-count=1"])
    if rc != 0 or rc2 != 0:
        print("★ ★★ 还原后基线不绿 ⇒ 有残留")
        return 1
    print("★ 还原后基线仍绿")

    failed = [r for r in results if not r[1]]
    if failed:
        print("★ ★★ %d 条变异未达预期" % len(failed))
        return 1
    print("★ ★★ 全部变异臂达标（★ %d 条）" % len(results))
    return 0


if __name__ == "__main__":
    sys.exit(main())
