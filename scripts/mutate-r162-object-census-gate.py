#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R162 轮的变异验证脚本（★ 期望集推导 + severity 词表 + 退役证据 + 覆盖门）。

# 这道脚本在钉什么

★ 仓内有一条纪律：**「证伪一道『保证』的唯一办法是【真的把它打破一次】」**。
★ R162 轮改了 R161 轮交付的对账器（★ schema 前缀差集、★ severity 词表），
★ 所以本脚本把**每一条新判据**都真的打破一次，★ 确认对应的门会红。

★ ★★ 三条纪律（★ 同族脚本的既有约定）：

① ★ **「红了」≠「验到了你想验的那条腿」** ⇒ 每条变异都**指名**它必须红在哪条腿上
   （★ 上游把那条腿改名/删除，★ 剩余腿照样能红 ⇒ 脚本照样报「验证通过」）。
② ★ **编译红不算数**（★ 只说明代码坏了，★ 不说明判据有牙）⇒ 只认**断言红**。
③ ★ **还原必须一字不差** ⇒ 还原后**逐字 md5 比对**。

# 用法

    python3 scripts/mutate-r162-object-census-gate.py

# ★ 残留声明（★ `scripts/audit/mutation_residue_guard.py` 会按它检查残留）

    TARGET = REPO / "scripts/audit/db-object-census.py"
    TARGET = REPO / "scripts/ursmcheck/db_object_census_gate_test.go"
    TARGET = REPO / "scripts/audit/asset/db-object-gap-ledger.tsv"
    TARGET = REPO / "scripts/audit/asset/db-object-retired.tsv"
    TARGET = REPO / "scripts/audit/asset/query-index-requirements.tsv"
"""

import hashlib
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]

# ★ 残留门按这两个声明收集「本脚本会写的文件」
TARGET = REPO / "scripts/audit/db-object-census.py"
TARGET = REPO / "scripts/ursmcheck/db_object_census_gate_test.go"
TARGET = REPO / "scripts/audit/asset/db-object-gap-ledger.tsv"
TARGET = REPO / "scripts/audit/db-object-census.py"
TARGET = REPO / "scripts/audit/asset/query-index-requirements.tsv"

LEDGER_REL = "scripts/audit/asset/db-object-gap-ledger.tsv"
RETIRED_REL = "scripts/audit/asset/db-object-retired.tsv"
REQS_REL = "scripts/audit/asset/query-index-requirements.tsv"
CENSUS_REL = "scripts/audit/asset/db-object-census.tsv"
PY_TOOL_REL = "scripts/audit/db-object-census.py"
GO_GATE_REL = "scripts/ursmcheck/db_object_census_gate_test.go"

GO_ENV = {
    "PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:" + str(pathlib.Path.home() / "sdk/go/bin"),
    "GOPROXY": "https://goproxy.cn,direct",
    "GOMODCACHE": str(pathlib.Path.home() / "sdk/gomodcache"),
    "HOME": str(pathlib.Path.home()),
}


def md5(path: pathlib.Path) -> str:
    return hashlib.md5(path.read_bytes()).hexdigest()


def run(cmd, cwd=REPO):
    env = {**dict(__import__("os").environ), **GO_ENV}
    p = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True, env=env)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


# ─────────────────────────── 变异定义 ───────────────────────────
# (名称, 相对路径, 旧文本, 新文本, 期望红的那条腿, 怎么判红)
MUTATIONS = [
    (
        "M1-PY-DROP-PREFIX",
        PY_TOOL_REL,
        "            dropped.add(_bare_name(m.group(1)))",
        "            dropped.add(_strip_name(m.group(1)))",
        "python-selftest:expect/excludes-schema-prefixed-drop",
        "selftest",
    ),
    (
        "M2-GO-DROP-PREFIX",
        GO_GATE_REL,
        "		dropped[dbObjBareName(m[1])] = true",
        "		dropped[dbObjStripName(m[1])] = true",
        "go:TestDbObjectExpectSetMatchesIndependentDerivation",
        "go",
    ),
    (
        "M3-PY-SEVERITY-VOCAB",
        PY_TOOL_REL,
        '        return "prod-full-gap"',
        '        return "production-gap"',
        "python-selftest:severity/mis-mis-mis",
        "selftest",
    ),
    (
        "M4-GO-EXPECT-NO-DROP-SUBTRACT",
        GO_GATE_REL,
        "	out := map[string][2]string{}\n	for k, v := range created {\n		if !dropped[k] {",
        "	out := map[string][2]string{}\n	for k, v := range created {\n		if true {",
        "go:TestDbObjectRetiredEvidenceLedger",
        "go",
    ),
    (
        "M5-LEDGER-CELL-TAMPER",
        LEDGER_REL,
        None,  # ★ 特殊处理：★ 见 apply_mutation
        None,
        "go:TestDbObjectGapLedgerIsFullyRecomputable",
        "go",
    ),
    (
        "M7-REQS-LIE-COVERED",
        REQS_REL,
        "attachments.presence\tadmin/data_lifecycle_attachments.go:93\tsession_turns\tattachment_count\t\tknown-gap\t",
        "attachments.presence\tadmin/data_lifecycle_attachments.go:93\tsession_turns\tattachment_count\t\tcovered\t",
        "go:TestQueryIndexCoverageRequirements",
        "go",
    ),
    (
        "M8-REQS-FILTER-COL-BROKEN",
        REQS_REL,
        "sessions.detail.point\tadmin/session_detail_v2.go:539\tsessions\ttenant_id,session_id\t\tcovered\t",
        "sessions.detail.point\tadmin/session_detail_v2.go:539\tsessions\ttenant_id,no_such_column_xyz\t\tcovered\t",
        "go:TestQueryIndexCoverageRequirements",
        "go",
    ),
    (
        "M9-CENSUS-UNRELATED-COLUMN",
        CENSUS_REL,
        None,  # ★ 特殊处理：★ 见 apply_mutation
        None,
        "green",
        "go",
    ),
    (
        "M6-CONTROL-COMMENT-ONLY",
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
    if name == "M9-CENSUS-UNRELATED-COLUMN":
        # ★ 改一个【与清单无关】的索引的列序列 ⇒ ★ 判定结果不应变 ⇒ ★ 门应仍绿（★ 对照臂）
        lines = original.decode("utf-8").split("\n")
        hit = False
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) == 17 and cols[1] == "idx" and cols[4] == "idx_sessions_status":
                cols[15] = "status,mutated_unrelated_column"
                lines[i] = "\t".join(cols)
                hit = True
                break
        if not hit:
            return original, False
        path.write_bytes("\n".join(lines).encode("utf-8"))
        return original, True
    if name == "M5-LEDGER-CELL-TAMPER":
        # ★ 台账里把第一行的 211 格子从 present 改成 missing（★ 手工定位，★ 不靠 grep -c）
        lines = original.decode("utf-8").split("\n")
        hit = False
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) >= 8 and cols[5] == "present":
                cols[5] = "missing"
                lines[i] = "\t".join(cols)
                hit = True
                break
        if not hit:
            return original, False
        path.write_bytes("\n".join(lines).encode("utf-8"))
        return original, True
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
        rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestQueryIndexCoverage", "-count=1", "-v"])
        return rc == 0 and ("--- PASS: TestQueryIndexCoverageRequirements" in out), out
    if kind == "go":
        # ★ 腿名带 "go:" 前缀只是给人看的（★ 脚本自述的腿名），★ -run 只认函数名
        fn = leg.split(":", 1)[1] if leg.startswith("go:") else leg
        rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", fn, "-count=1", "-v"])
        return rc != 0 and ("--- FAIL: %s" % fn) in out, out
    if kind == "selftest+go":
        rc1, out1 = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
        rc2, out2 = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject", "-count=1"])
        return rc1 == 0 and rc2 == 0, out1 + out2
    return False, ""


def main():
    # ★ 基线必须绿（★ 否则「变异后红」毫无意义）
    rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
    if rc != 0:
        sys.stderr.write("★ 基线 selftest 不绿 ⇒ 停\n" + out[-2000:])
        return 1
    rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject", "-count=1"])
    if rc != 0:
        sys.stderr.write("★ 基线 go 门不绿 ⇒ 停\n" + out[-2000:])
        return 1
    print("★ 基线绿（★ selftest + go 门）")

    results = []
    for name, rel, old, new, leg, kind in MUTATIONS:
        original, ok = apply_mutation(name, rel, old, new)
        if not ok:
            results.append((name, False, "★ 变异未施加"))
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
    rc2, out2 = run(["go", "test", "./scripts/ursmcheck/", "-run", "TestDbObject", "-count=1"])
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
