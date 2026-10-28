#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1046「掏空门的子构建 ⇒ 必须隔离到专用 GOCACHE」的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1046_hollowout_gocache_isolation.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 背景（★ 实测，2026-10-28）：

★★ 掏空法门要让读方跑在「**被改瞎的副本**」上 ⇒ ⇒ 它会**现编译**被读包的测试二进制
★★ ⇒ ⇒ 编译发生在**子进程**里 ⇒ ⇒ 子进程用的是**用户那个全局 `GOCACHE`**。

★★★ 实测的泄漏量（★ 同一棵树上跑两遍）：
★★   第 1 遍（冷）：★ 全局 GOCACHE **+1281 MB** ★ 213.35s
★★   第 2 遍（同一棵树、热）：★ 全局 GOCACHE **+0 MB** ★ 153.99s
★★★ ⇒ ⇒ 增长**不是「每次跑都漏」，而是「【树变一次就漏一批】」** ——
★★★ 因为门在跑的过程中**改写仓内 `.tsv` 的内容**（★ `//go:embed` 在编译期把内容
★★★ 嵌进二进制 ⇒ ⇒ 内容一变 ⇒ ⇒ action ID 就变）⇒ ⇒ 树一变，门就要为**每个读方包**
★★★ **重新编译出全新的一套二进制** ⇒ ⇒ 旧的**永远不再被命中** ⇒ ⇒ 事实上的垃圾
★★★ ⇒ ⇒ 却躺在用户那个「跨项目、跨天、Go 只按 5 天 trim」的全局缓存里。
★★★ ⇒ ⇒ 实测本机全局 GOCACHE 一度到 **72 GB** ⇒ ⇒ 磁盘 100% 满 ⇒ ⇒ 全量
★★★ `go test ./...` **编译阶段**就报 `no space left on device`
★★★ ⇒ ⇒ ★★ **红的原因不指向真正的原因**（★ 真正的原因是磁盘，不是门有洞）。

★★ 本台覆盖的 5 个臂（★ 每个臂对应一条「断言没牙齿」的可能）：

★   B00 ★★★ **惰性变异筛查**：★ 先跑一遍【未变异】的基线 ⇒ 确认它本来就绿
★        ⇒ ⇒ 否则「变异后红」可能只是【基线本来就红】。
★   M01 ★★★ 把 `GOCACHE` 的**覆盖**去掉（★ 子构建回落用环境里的全局缓存）
★        ⇒ 期望：「隔离」那条**转红** ⇒ ⇒ 证明它咬住了「污染全局缓存」这个形态。
★   M02 ★★ 把专用缓存目录挪到**没被 `.gitignore` 覆盖**的路径
★        ⇒ 期望：「不入库」那条**转红** ⇒ ⇒ 证明「专用目录必须落在已忽略路径下」
★        ⇒ ⇒ 这条断言不是装饰（★ 否则专用缓存会被提交进仓库）。
★   M03 ★★ 让剪枝**永不生效**（★ 专用缓存无界增长 ⇒ ⇒ 又会撑爆磁盘）
★        ⇒ 期望：「剪枝」那条**转红** ⇒ ⇒ 证明剪枝真的有牙齿。
★   M04 ★★★ **掏空全部断言**（★ 把 `t.Fatalf` 改成 `t.Logf`）★ **再叠加 M01**
★        ⇒ 期望：**全部转绿**
★        ⇒ ⇒ ⇒ 证明这三条断言**合起来才是必需的**（★ 没有它们，★ 「把隔离改没了」
★        ⇒ ⇒ ⇒ 也能全绿 ⇒ ⇒ ⇒ **功能被改没了而门全绿**）。

★★ 纪律（★ 与 scripts/audit/mutate_r1045_reconciliation_diff_write_truth.py 同构）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ④ 环境指纹对拍；
★   ⑤ 每个变异都必须自带【效果探针】；
★★ ⑥ ★★★ **「没牙」要先过【惰性变异】筛查**；
★★ ⑦ ★★★ 逐条判定【是哪一条断言红了】⇒ 只看 rc 不算数（★ rc=1 可能来自任何一条）。
"""

import hashlib
import os
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

TARGET_REL = "scripts/ursmcheck/hollowout_gocache_test.go"
TARGET = os.path.join(ROOT, TARGET_REL)

PKG = "./scripts/ursmcheck/"
TESTS = (
    "TestHollowoutBuildUsesIsolatedGoCache"
    "|TestHollowoutGoCacheDirIsNotTracked"
    "|TestPruneHollowGoCacheRespectsSizeCap"
)

# ── M01 锚点：去掉 GOCACHE 的覆盖（★ 子构建回落用环境里的全局缓存）─────────
M01_NEEDLE = """		env = append(env, "GOCACHE="+dir)"""
M01_REPL = """		_ = dir // MUTATION M01: 不覆盖 GOCACHE ⇒ 子构建写回用户的全局缓存"""

# ── M02 锚点：把专用缓存目录挪到【没被 .gitignore 覆盖】的路径 ────────────
M02_NEEDLE = """	hollowGoCacheRelDir = ".build-local/.gocache-hollowout\""""
M02_REPL = """	hollowGoCacheRelDir = ".hollowout-gocache" // MUTATION M02: 不在 .build-local/ 下"""

# ── M03 锚点：让剪枝永不生效（★ 专用缓存无界增长）────────────────────────
M03_NEEDLE = """	if size <= maxBytes {
		return false, nil
	}"""
M03_REPL = """	if size <= maxBytes || true { // MUTATION M03: 永不剪枝
		return false, nil
	}"""

# ── M04 锚点：掏空全部断言（★ t.Fatalf ⇒ t.Logf ⇒ 永远绿）────────────────
#
# ★★★ **这里必须用【块注释】而不是行注释**（★ 2026-10-28 实测踩到）：
# ★★ 行注释 `//` 会把**该行余下的内容整行吃掉** ⇒ ⇒ 而 `t.Fatalf(...)` 的
# ★★ 调用参数**就在同一行**（★ 或跨行续接）⇒ ⇒ 变异后是
# ★★ 「`t.Logf( // …` 后面整行没了」⇒ ⇒ **编译红**（★ 实测报
# ★★ `expected operand, found '}'`）⇒ ⇒ ★★ **编译红 ≠ 断言红 ⇒ 不算数**
# ★★ ⇒ ⇒ ★★★ **M04 这一臂在修好之前等于没跑**（★ 它「失败」的原因不是
# ★★★ 「断言被掏空后全绿」，★ 而是自己把代码改坏了）。
# ★★ ⇒ ⇒ 块注释 `/* … */` **不吞行尾** ⇒ ⇒ 变异后仍能编译 ⇒ ⇒ 才是合法变异。
M04_NEEDLE = "t.Fatalf("
M04_REPL = "t.Logf(/* MUTATION M04: 断言被掏空 */"


# ───────────────────────── 基础工具 ─────────────────────────


def snap(path):
    if not os.path.exists(path):
        return None
    with open(path, "rb") as f:
        return f.read()


def md5(data):
    if data is None:
        return "MISSING"
    return hashlib.md5(data).hexdigest()


def go_env():
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + env.get("PATH", "")
    return env


def run_tests():
    """跑一次目标测试，返回 (rc, 输出文本)。"""
    proc = subprocess.run(
        ["go", "test", PKG, "-run", TESTS, "-count=1", "-v"],
        cwd=ROOT,
        env=go_env(),
        capture_output=True,
        text=True,
        timeout=900,
    )
    return proc.returncode, proc.stdout + proc.stderr


def failed_tests(output):
    """返回失败测试名列表（★ 逐条指名，不只看 rc）。"""
    out = []
    for line in output.splitlines():
        line = line.strip()
        if line.startswith("--- FAIL: "):
            out.append(line[len("--- FAIL: "):].split(" ")[0])
    return out


def compile_ok():
    """变异必须仍能编译（★ 编译红 ≠ 断言红 ⇒ 不算数）。"""
    proc = subprocess.run(
        ["go", "vet", PKG],
        cwd=ROOT,
        env=go_env(),
        capture_output=True,
        text=True,
        timeout=900,
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


def replace_once(text, needle, repl):
    n = text.count(needle)
    if n != 1:
        raise AssertionError("锚点命中 %d 次（期望恰好 1 次）⇒ 变异没有落在唯一一处" % n)
    return text.replace(needle, repl, 1)


# ───────────────────────── 台账 ─────────────────────────

LEDGER = []


def record(arm, expect, got, ok):
    LEDGER.append({"arm": arm, "expect": expect, "got": got, "ok": ok})
    print("[%s] %s" % ("OK " if ok else "FAIL", arm))
    print("       期望：%s" % expect)
    print("       实测：%s" % got)


def main() -> int:
    if not os.path.exists(TARGET):
        print("目标文件不存在：%s" % TARGET, file=sys.stderr)
        return 2

    original = snap(TARGET)
    base_md5 = md5(original)
    print("目标：%s" % TARGET_REL)
    print("基线 md5：%s" % base_md5)
    print("")

    def write(text):
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(text)

    def restore():
        with open(TARGET, "wb") as f:
            f.write(original)
        return md5(snap(TARGET)) == base_md5

    # ── 纪律 ⑥：惰性变异筛查 —— 先确认基线本来就绿 ──────────────
    rc, out = run_tests()
    if rc != 0 or failed_tests(out):
        record(
            "B00 基线（惰性变异筛查）",
            "rc=0 且 0 条测试失败（★ 基线本来就绿 ⇒ 否则「变异后红」可能只是基线本来就红）",
            "rc=%s，红：%s" % (rc, failed_tests(out) or "无"),
            False,
        )
        return 1
    record("B00 基线（惰性变异筛查）", "rc=0 且 0 条测试失败",
           "rc=0，三条断言全绿", True)

    try:
        # ── M01：去掉 GOCACHE 覆盖 ⇒ 隔离那条必须转红 ──────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        write(replace_once(text, M01_NEEDLE, M01_REPL))

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestHollowoutBuildUsesIsolatedGoCache" in failed
            and "TestHollowoutGoCacheDirIsNotTracked" not in failed
            and "TestPruneHollowGoCacheRespectsSizeCap" not in failed
        )
        record(
            "M01 去掉 GOCACHE 覆盖（子构建回落用全局缓存）",
            "仍能编译；★ 且**只有**「BuildUsesIsolatedGoCache」转红"
            "（★ 另外两条必须照常绿 ⇒ 证明不是「全都红」）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M01 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M02：专用目录挪到未被忽略的路径 ⇒ 不入库那条必须转红 ─
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        write(replace_once(text, M02_NEEDLE, M02_REPL))

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestHollowoutGoCacheDirIsNotTracked" in failed
            and "TestHollowoutBuildUsesIsolatedGoCache" not in failed
        )
        record(
            "M02 专用缓存目录挪到未被 .gitignore 覆盖的路径",
            "仍能编译；★ 「GoCacheDirIsNotTracked」转红"
            "（★ ⇒ 证明「专用目录必须落在已忽略路径下」这条不是装饰）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M02 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M03：剪枝永不生效 ⇒ 剪枝那条必须转红 ──────────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        write(replace_once(text, M03_NEEDLE, M03_REPL))

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestPruneHollowGoCacheRespectsSizeCap" in failed
            and "TestHollowoutBuildUsesIsolatedGoCache" not in failed
        )
        record(
            "M03 让剪枝永不生效（专用缓存无界增长）",
            "仍能编译；★ 「PruneHollowGoCacheRespectsSizeCap」转红"
            "（★ ⇒ 证明剪枝真的有牙齿 ⇒ ⇒ 不然专用缓存只是换了个地方撑爆磁盘）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M03 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M04：掏空全部断言 + M01 ⇒ 全部转绿 ───────────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        text = replace_once(text, M01_NEEDLE, M01_REPL)
        n = text.count(M04_NEEDLE)
        if n < 1:
            print("M04 锚点（t.Fatalf）没找到 ⇒ 中止", file=sys.stderr)
            return 1
        text = text.replace(M04_NEEDLE, M04_REPL)
        write(text)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = ok_build and rc == 0 and not failed
        record(
            "M04 掏空全部断言 + M01 的「去掉隔离」变异",
            "仍能编译；★ **全部测试转绿**（★ ⇒ ⇒ 证明这三条断言**合起来才是必需的**："
            "没有它们，★ 「把隔离改没了」也能全绿 ⇒ ★★ 功能被改没了而门全绿）",
            "编译=%s，rc=%s，红：%s%s（★ 掏空了 %d 处 t.Fatalf）" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数", n),
            ok,
        )
        if not restore():
            print("M04 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

    finally:
        # ── 纪律 ①：还原必须按原路径做 md5 逐字校验 ────────────
        with open(TARGET, "wb") as f:
            f.write(original)
        now = md5(snap(TARGET))
        if now != base_md5:
            print("★ 最终还原校验失败：%s != %s" % (now, base_md5), file=sys.stderr)
            return 1

    print("")
    passed = sum(1 for r in LEDGER if r["ok"])
    print("变异台账：%d/%d 通过" % (passed, len(LEDGER)))
    for r in LEDGER:
        print("  %s %s" % ("OK " if r["ok"] else "FAIL", r["arm"]))
    print("还原校验：md5 %s（与基线一致）" % base_md5)
    return 0 if passed == len(LEDGER) else 1


if __name__ == "__main__":
    sys.exit(main())
