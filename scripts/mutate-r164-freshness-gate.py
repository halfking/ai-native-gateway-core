#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R164 轮的变异验证脚本（★ 底账新鲜度 / 结构漂移 vs 指标漂移 / 跨语言口径互证）。

# 这道脚本在钉什么

★ 仓内有一条纪律：**「证伪一道『保证』的唯一办法是【真的把它打破一次】」**。
★ R164 轮新增了：★ `verify` 模式、★ 采集时刻透传、★ 结构/指标两套口径、★ 一道新 Go 门
⇒ ⇒ 所以本脚本把**每一条新判据**都真的打破一次，★ 确认对应的门会红。

★ ★★ 三条纪律（★ 同族脚本的既有约定）：

① ★ **「红了」≠「验到了你想验的那条腿」** ⇒ 每条变异都**指名**它必须红在哪条腿上。
② ★ **编译红不算数**（★ 只说明代码坏了，★ 不说明判据有牙）⇒ 只认**断言红**。
③ ★ **还原必须一字不差** ⇒ 还原后**逐字 md5 比对**。

★ ★★ 本轮最要紧的一条（R164 自带的教训）：

★★ **「兜底值不许长得像一个合法值」** ⇒ ★ `collected_at` 的兜底写成 `unknown` ⇒ ⇒
★★ ⇒ **「透传失败」被伪装成「采集时刻未知」** ⇒ ⇒ ⇒ 本脚本要验：
★★ ⇒ **把兜底换回 `unknown` ⇒ ⇒ 门必须立刻红**（★ 否则这个缺陷可以悄悄回来）。

# 用法

    python3 scripts/mutate-r164-freshness-gate.py

# ★ 残留声明（★ `scripts/audit/mutation_residue_guard.py` 会按它检查残留）

    TARGET = REPO / "scripts/audit/db-object-census.py"
    TARGET = REPO / "scripts/ursmcheck/db_object_freshness_gate_test.go"
    TARGET = REPO / "scripts/audit/asset/db-object-census.tsv"
    TARGET = REPO / "scripts/audit/asset/db-object-columns.tsv"
    TARGET = REPO / "docs/audit/2026-10-27-R164-底账新鲜度与漂移判据.md"
"""

import hashlib
import os
import pathlib
import subprocess
import sys

# ★★ r1047 轮（★ 2026-10-28）：★ 子进程走【专用 GOCACHE】⇒ ⇒ 不再往用户的全局缓存里
# ★★ 写「随树变化而失效」的一次性产物（★ 理由与实测读数见 `scripts/lib/gocache.py`
# ★★ 与 `internal/gocache` 的包注释）。
# ★★ **两条路径都试**（★ ★★ 深度无关：★ 本文件在 scripts/ 或 scripts/audit/ 下）★★
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402
gocache.apply_to_environ("mutate")

REPO = pathlib.Path(__file__).resolve().parents[1]

# ★ 残留门按这几个声明收集「本脚本会写的文件」
TARGET = REPO / "scripts/audit/db-object-census.py"
TARGET = REPO / "scripts/ursmcheck/db_object_freshness_gate_test.go"
TARGET = REPO / "scripts/audit/asset/db-object-census.tsv"
TARGET = REPO / "scripts/audit/asset/db-object-columns.tsv"
TARGET = REPO / "docs/audit/2026-10-27-R164-底账新鲜度与漂移判据.md"

PY_TOOL_REL = "scripts/audit/db-object-census.py"
GO_GATE_REL = "scripts/ursmcheck/db_object_freshness_gate_test.go"
CENSUS_REL = "scripts/audit/asset/db-object-census.tsv"
COLUMNS_REL = "scripts/audit/asset/db-object-columns.tsv"
AUDIT_REL = "docs/audit/2026-10-27-R164-底账新鲜度与漂移判据.md"

GO_ENV = {
    "PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:" + str(pathlib.Path.home() / "sdk/go/bin"),
    "GOPROXY": "https://goproxy.cn,direct",
    "GOMODCACHE": str(pathlib.Path.home() / "sdk/gomodcache"),
    "HOME": str(pathlib.Path.home()),
}

# ★★ Go 侧要跑的判据（★ 变异台只跑这批，★ 别把慢门拖进来）
GO_RUN = (
    "TestDbObjectFreshness|TestDbObjectCensusCarries|TestDbObjectCensusHeader|"
    "TestDbObjectDrift|TestDbObjectVolatile|TestDbObjectColumns|"
    "TestDbObjectCensusSnapshotIsWellFormed|TestDbObjectColumnsSnapshotIsWellFormed"
)

# ─────────────────────────── 锚点 ───────────────────────────

# ★★ R164 自带的一条纪律：★★ **兜底值不许长得像一个合法值** ⇒ ★ `collected_at` 的兜底
# ★★ 写成 `unknown` ⇒ ⇒ 「透传失败」被伪装成「采集时刻未知」⇒ ⇒ ⇒ 所以：
# ★★   · 产物侧（★ 头部字段被改坏）⇒ ★ 由 Go 门 G15/G21 抓
# ★★   · 工具侧（★ 兜底被换成一个合法时间戳）⇒ ★ 由 Python 自证抓
# ★★ ⇒ ★ 两层各自守自己那侧 ⇒ ★ 不用跨语言互调（★ ⇒ 没有「python3 缺失 ⇒ 门空转」的风险）。

# ① 工具侧：★ 兜底值换成一个【看起来合法的】时间戳 ⇒ ★ Python 自证红
# ★★ 锚点必须带上下文（★ 因为 cmd_bundle 与 cmd_columns_bundle 各有一处同样的兜底行
# ★★ ⇒ ⇒ 只用单行会命中 2 次 ⇒ ⇒ 锚点必须唯一）。
PY_UNKNOWN_ANCHOR = '''                hashlib.sha256("\\n".join(rows).encode("utf-8")).hexdigest(),
                per_env_collected.get(env) or "unknown",'''
PY_UNKNOWN_BROKEN = '''                hashlib.sha256("\\n".join(rows).encode("utf-8")).hexdigest(),
                per_env_collected.get(env) or "2026-01-01T00:00:00+00:00",'''

# ② Python 侧 volatile 列清单（★ 加一列 ⇒ 与 Go 侧分叉 ⇒ G18 红）
PY_VOLATILE_ANCHOR = 'VOLATILE_CENSUS_COLUMNS = ("reltuples", "table_size", "total_size")'
PY_VOLATILE_BROKEN = 'VOLATILE_CENSUS_COLUMNS = ("reltuples", "table_size", "total_size", "definition")'

# ③ Go 侧 volatile 列清单（★ 少一列 ⇒ 与 Python 侧分叉 ⇒ G18 红）
GO_VOLATILE_ANCHOR = 'var dbObjFreshVolatile = []string{"reltuples", "table_size", "total_size"}'
GO_VOLATILE_BROKEN = 'var dbObjFreshVolatile = []string{"reltuples", "table_size"}'

# ④ verify 子命令（★ 改名 ⇒ 「快照过期」回到不可机器判 ⇒ G19 红）
PY_VERIFY_ANCHOR = '''    p = sub.add_parser(
        "verify",
        help="★ R164：★ 把快照与此刻的真实库对拍 ⇒ 报【结构漂移】（★ 退出码 0/1/2）",
    )'''
PY_VERIFY_BROKEN = '''    p = sub.add_parser(
        "verify-disabled",
        help="★ 变异：★ verify 模式被移除",
    )'''

# ⑤ verify 的结果串（★ 改名 ⇒ 退出码语义不可读 ⇒ G19 红）
PY_RESULT_ANCHOR = 'sys.stderr.write("RESULT: STRUCTURAL DRIFT\\n")'
PY_RESULT_BROKEN = 'sys.stderr.write("RESULT: DRIFT\\n")'

# ⑥ Go 侧把「只有指标在动」判成形态变了（★ ⇒ G17 红）
GO_METRIC_ANCHOR = '''		} else if dbObjFreshMetrics(sp) != dbObjFreshMetrics(lp) {
			d.MetricOnly = append(d.MetricOnly, id)
		}'''
GO_METRIC_BROKEN = '''		} else if dbObjFreshMetrics(sp) != dbObjFreshMetrics(lp) {
			d.Changed = append(d.Changed, id)
		}'''

# ⑦ 对照臂：★ 只加注释 ⇒ ★ 行为不变 ⇒ ★ 门必须仍绿
PY_CONTROL_ANCHOR = "def die(msg: str, code: int = 3):"
PY_CONTROL_BROKEN = """def die(msg: str, code: int = 3):
    # ★ 对照变异：★ 只加注释 ⇒ ★ 行为不变 ⇒ ★ 门必须仍绿"""


MUTATIONS = [
    (
        "M1-TOOL-FALLBACK-LOOKS-VALID",
        PY_TOOL_REL,
        PY_UNKNOWN_ANCHOR,
        PY_UNKNOWN_BROKEN,
        "selftest:bundle/fallback-is-explicit-unknown",
        "selftest",
    ),
    (
        "M2-PY-VOLATILE-EXPANDS",
        PY_TOOL_REL,
        PY_VOLATILE_ANCHOR,
        PY_VOLATILE_BROKEN,
        "go:TestDbObjectVolatileColumnSetMatchesPythonSide",
        "go",
    ),
    (
        "M3-GO-VOLATILE-SHRINKS",
        GO_GATE_REL,
        GO_VOLATILE_ANCHOR,
        GO_VOLATILE_BROKEN,
        "go:TestDbObjectVolatileColumnSetMatchesPythonSide",
        "go",
    ),
    (
        "M4-VERIFY-MODE-REMOVED",
        PY_TOOL_REL,
        PY_VERIFY_ANCHOR,
        PY_VERIFY_BROKEN,
        "go:TestDbObjectCensusToolStillHasVerifyMode",
        "go",
    ),
    (
        "M5-RESULT-STRING-RENAMED",
        PY_TOOL_REL,
        PY_RESULT_ANCHOR,
        PY_RESULT_BROKEN,
        "go:TestDbObjectCensusToolStillHasVerifyMode",
        "go",
    ),
    (
        "M6-GO-METRIC-AS-CHANGED",
        GO_GATE_REL,
        GO_METRIC_ANCHOR,
        GO_METRIC_BROKEN,
        "go:TestDbObjectDriftClassifierSeparatesStructureFromMetrics",
        "go",
    ),
    (
        "M7-AUDIT-DOC-ROWCOUNT",
        AUDIT_REL,
        "local=2667 / fresh=2487 / 252=3473 / 211=2853",
        "local=2667 / fresh=2487 / 252=3473 / 211=9999",
        "go:TestDbObjectFreshnessAuditDocQuotesTheLedger",
        "go",
    ),
    (
        "M8-CENSUS-SHA-TAMPER",
        CENSUS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectCensusHeaderMatchesRecomputedSha",
        "go",
    ),
    (
        "M9-CENSUS-COLLECTED-AT-DROPPED",
        CENSUS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectCensusCarriesPerEnvCollectedAt",
        "go",
    ),
    (
        "M10-CENSUS-COLLECTED-AT-UNKNOWN",
        CENSUS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectCensusCarriesPerEnvCollectedAt",
        "go",
    ),
    (
        "M11-COLUMNS-COLLECTED-AT-DROPPED",
        COLUMNS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectColumnsCarryPerEnvCollectedAt",
        "go",
    ),
    (
        "M12-COLUMNS-SHA-TAMPER",
        COLUMNS_REL,
        None,  # ★ 特殊处理
        None,
        "go:TestDbObjectColumnsSnapshotIsWellFormed",
        "go",
    ),
    (
        "M13-CONTROL-COMMENT-ONLY",
        PY_TOOL_REL,
        PY_CONTROL_ANCHOR,
        PY_CONTROL_BROKEN,
        "green",
        "go",
    ),
]


def md5(path):
    return hashlib.md5(path.read_bytes()).hexdigest()


def run(cmd):
    proc = subprocess.run(
        cmd, cwd=str(REPO), capture_output=True, text=True, env={**os.environ, **GO_ENV}
    )
    return proc.returncode, proc.stdout + proc.stderr


def apply_mutation(name, rel, old, new):
    """★ 施加变异 ⇒ 返回 (原字节, 是否成功)。"""
    path = REPO / rel
    original = path.read_bytes()
    if name == "M8-CENSUS-SHA-TAMPER":
        # ★ 篡改底账里的一行数据（★ 改 definition ⇒ 行集变了 ⇒ header sha 对不上 ⇒ G16 红）
        lines = original.decode("utf-8").split("\n")
        hit = False
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) != 17:
                continue
            cols[12] = "TAMPERED"
            lines[i] = "\t".join(cols)
            hit = True
            break
        if not hit:
            return original, False
        path.write_bytes("\n".join(lines).encode("utf-8"))
        return original, True
    if name in ("M9-CENSUS-COLLECTED-AT-DROPPED", "M10-CENSUS-COLLECTED-AT-UNKNOWN"):
        # ★ 篡改底账【头部】的 collected_at ⇒ ⇒ G15 必须红
        text = original.decode("utf-8")
        if "collected_at=" not in text:
            return original, False
        if name == "M9-CENSUS-COLLECTED-AT-DROPPED":
            # ★ 把第一处 collected_at=… 整段删掉（★ 头部少字段 ⇒ 「采集时刻查不到」）
            import re as _re
            new_text, n = _re.subn(r"\s+collected_at=\S+", "", text, count=1)
        else:
            # ★ 把第一处 collected_at 的值换成 unknown（★ 「长得像有值」的坏值）
            import re as _re
            new_text, n = _re.subn(r"collected_at=\S+", "collected_at=unknown", text, count=1)
        if n != 1:
            return original, False
        path.write_text(new_text, encoding="utf-8")
        return original, True
    if name == "M11-COLUMNS-COLLECTED-AT-DROPPED":
        # ★ 篡改列底账【头部】的 collected_at ⇒ ⇒ G21 必须红
        text = original.decode("utf-8")
        if "collected_at=" not in text:
            return original, False
        import re as _re
        new_text, n = _re.subn(r"\s+collected_at=\S+", "", text, count=1)
        if n != 1:
            return original, False
        path.write_text(new_text, encoding="utf-8")
        return original, True
    if name == "M12-COLUMNS-SHA-TAMPER":
        # ★ 篡改列底账的一行 ⇒ 头部的 sha256 与实际行集不符 ⇒ R163 的 sha 门红
        lines = original.decode("utf-8").split("\n")
        hit = False
        for i, line in enumerate(lines):
            if line.startswith("#") or not line.strip():
                continue
            cols = line.split("\t")
            if len(cols) != 4:
                continue
            cols[3] = "tampered_column"
            lines[i] = "\t".join(cols)
            hit = True
            break
        if not hit:
            return original, False
        path.write_bytes("\n".join(lines).encode("utf-8"))
        return original, True
    text = original.decode("utf-8")
    if text.count(old) != 1:
        sys.stderr.write(
            "★ 锚点在 %s 里命中 %d 次（★ 必须恰好 1 次）\n" % (rel, text.count(old))
        )
        return original, False
    path.write_text(text.replace(old, new, 1), encoding="utf-8")
    return original, True


def restore(rel, original):
    (REPO / rel).write_bytes(original)


def check_leg(kind, leg, name):
    """★ 判「红」必须红在**指名的那条腿**上。"""
    if kind == "selftest":
        rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
        needle = leg.split(":", 1)[1] if leg.startswith("selftest:") else leg
        return rc != 0 and ("FAIL %s" % needle) in out, out
    if kind == "go" and leg == "green":
        rc, out = run(
            ["go", "test", "./scripts/ursmcheck/", "-run", GO_RUN, "-count=1", "-v"]
        )
        return rc == 0 and ("--- PASS: TestDbObjectCensusCarriesPerEnvCollectedAt" in out), out
    if kind == "go":
        fn = leg.split(":", 1)[1] if leg.startswith("go:") else leg
        rc, out = run(["go", "test", "./scripts/ursmcheck/", "-run", fn, "-count=1", "-v"])
        return rc != 0 and ("--- FAIL: %s" % fn) in out, out
    return False, ""


def main():
    # ★ 基线必须绿（★ 否则「变异后红」毫无意义）
    rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
    if rc != 0:
        sys.stderr.write("★ 基线 selftest 不绿 ⇒ 停\n" + out[-2000:])
        return 1
    rc, out = run(
        ["go", "test", "./scripts/ursmcheck/", "-run", GO_RUN, "-count=1"]
    )
    if rc != 0:
        sys.stderr.write("★ 基线 go 门不绿 ⇒ 停\n" + out[-2000:])
        return 1
    print("★ 基线绿（★ selftest + R164 新门）")

    results = []
    for name, rel, old, new, leg, kind in MUTATIONS:
        original, ok = apply_mutation(name, rel, old, new)
        if not ok:
            results.append((name, False, "★ 变异未施加"))
            print("%-36s %-6s ✘  ★ 变异未施加" % (name, "GREEN" if leg == "green" else "RED"))
            continue
        try:
            red, out = check_leg(kind, leg, name)
            detail = ""
            if not red:
                detail = "★ 该红的地方没红（★ 期望红在 %s）" % leg
            results.append((name, red, detail))
            want = "GREEN" if leg == "green" else "RED"
            print("%-36s %-6s %s  %s" % (name, want, "✔" if red else "✘", detail))
            if not red:
                print(out[-1500:])
        finally:
            restore(rel, original)
            after = md5(REPO / rel)
            before = hashlib.md5(original).hexdigest()
            if after != before:
                results.append((name, False, "★ 还原后 md5 不一致 ⇒ 残留！"))
                print("★ ★★ 还原失败：%s（★ 残留！）" % rel)

    print("\n★ 变异结果：")
    for name, ok, detail in results:
        print("  %-36s %s  %s" % (name, "PASS" if ok else "FAIL", detail))

    rc, out = run([sys.executable, "scripts/audit/db-object-census.py", "selftest"])
    rc2, out2 = run(
        ["go", "test", "./scripts/ursmcheck/", "-run", GO_RUN, "-count=1"]
    )
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
