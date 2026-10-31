#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1045「对账差异写入失败 ⇒ 账本不许说谎」的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1045_reconciliation_diff_write_truth.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 背景（★ 实测，2026-10-28 07:32:39~07:32:42）：

★★ 542 条 `INSERT INTO stats_reconciliation_diffs` 全部失败（★ 那次的**近因**是
★★ 本机库缺 `tenant_id` 列 ⇒ ★ 已由 r1043 补迁移修掉；★★ 但**让这族失败静默
★★ 无声的机制**还在仓里 ⇒ ★★ 本台钉的就是它）。

★★ 那一轮在 `stats_reconciliation_runs` 账本里记的是：
★★ `status=completed, rows_compared=0, rows_repaired=0, diff_count=0, error=''`
★★ ⇒ 「0 差异、没有错误、已完成」⇒ ★★ **账本 / 管理界面 / 指标三处都在说谎**。
★★ 机制：写入失败被 `slog.Warn` + `continue` 吞掉 ⇒ `totalDiffs` 不加 ⇒
★★ `diff_count = diffs - repaired = 0` ⇒ ★★ 而那条差异**从未进表** ⇒ 永久丢失。

★★ 本台覆盖的 3 个臂（★ 每个臂对应一条「断言没牙齿」的可能）：

★   M01 ★★★ 把修复**回退**成旧的 `slog.Warn` + `continue` ⇒ 期望：
★        「写入失败必须记成 failed」那条测试**转红**
★        ⇒ ⇒ 证明那条断言**真的咬住了**「失败被降级成 warn」这个形态。
★   M02 ★★ 保留「失败」但把 `error` 列写成**空串** ⇒ 期望：
★        同一条测试**转红** ⇒ ⇒ 证明「error 列必须非空」这条断言有牙齿
★        ⇒ ⇒ ★★ 顺带证明用 `pgxmock.AnyArg()` 代替它是**不够的**
★        ⇒ ⇒ （AnyArg 对任何值都过 ⇒ 那条断言就只是装饰）。
★   M03 ★★★ 让这一轮**永远失败**（★ 把「写入失败 ⇒ 返回错误」的条件改成恒真）
★        ⇒ 期望：**只有**反向守卫那条转红（★ 正向那条必须照常绿 ⇒
★        ⇒ ★★ 证明正向断言**单独**抓不住「永远失败」这种退化）。
★   M04 ★★★ **掏空反向守卫**：★ 在 M03 的同一个变异之上，★ **再把反向守卫那条
★        测试删掉** ⇒ 期望：**全部测试转绿**
★        ⇒ ⇒ ⇒ 证明反向守卫是**必需**的（★ 没有它，「把这一轮改成永远失败」
★        ⇒ ⇒ ⇒ 也能满足正向断言 ⇒ ⇒ ⇒ **功能被改没了而门全绿**）。

★★ 纪律（★ 与 scripts/audit/mutate_r1044_mobile_list_ux_duplicate_registry.py 同构）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ④ 环境指纹对拍；
★   ⑤ 每个变异都必须自带【效果探针】；
★★ ⑥ ★★★ **「没牙」要先过【惰性变异】筛查**——★ 先跑一遍【未变异】的基线，
★★      确认它本来就绿 ⇒ 否则「变异后红」可能只是【基线本来就红】；
★★ ⑦ ★★★ 逐条判定【是哪一条断言红了】⇒ 只看 rc 不算数（★ rc=1 可能来自任何一条）。
"""

import hashlib
import os
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

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

TARGET_REL = "domains/stats/reconciliation.go"
TARGET = os.path.join(ROOT, TARGET_REL)

TEST_REL = "domains/stats/reconciliation_test.go"
TEST = os.path.join(ROOT, TEST_REL)

PKG = "./domains/stats/"
TESTS = "TestReconciliationWorker_DiffWrite"

# ── M01 锚点：把「写入失败 ⇒ 返回错误」回退成旧的 warn + continue ──────────
M01_NEEDLE = """				return totalDiffs, autoRepaired, phantomDiffs, false,
					fmt.Errorf("record diff (tenant=%s dim=%s metric=%s): %w",
						key.tenantID, dimensionKey, d.metric, err)"""
M01_REPL = """				slog.Warn("failed to record diff", "error", err, "run_id", runID)
				continue"""

# ── M02 锚点：保留「失败」但把 error 列写成空串 ──────────────────────────
# ★ 唯一锚点：★ `resetPendingRepairs` + `finishRun` 这**两行**的组合在仓里只出现一次
# ★ （★ `finishRun(... "failed" ...)` 单独出现 3 次 ⇒ ★ 单行锚点会命中 3 处 ⇒ ★★ 变异
# ★ ★ 就没落在唯一一处了 ⇒ ★★ 必须用两行锚点把它钉死）。
M02_ANCHOR = """		w.resetPendingRepairs(ctx, runID)
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, err.Error())"""
M02_ANCHOR_REPL = """		w.resetPendingRepairs(ctx, runID)
		w.finishRun(ctx, runID, "failed", eventsSeen, rowsCompared, rowsRepaired, diffCount, sourceWatermark, "") // MUTATION M02"""

# ── M03 锚点：让这一轮**永远失败**（★ 把「写入失败」的条件改成恒真）────────
M03_NEEDLE = """			if err != nil {
				// ★★★ r1045（2026-10-28）：★ 写入失败**不再降级成 warn + continue**。"""
M03_REPL = """			if err != nil || true { // MUTATION M03: 让这一轮永远失败
				// ★★★ r1045（2026-10-28）：★ 写入失败**不再降级成 warn + continue**。"""

# ── M04 锚点：删掉反向守卫（那条「写入成功 ⇒ 仍然 completed」的测试）──────
M03_NEEDLE_START = "// TestReconciliationWorker_DiffWriteSuccessStillCompletes"
M03_NEEDLE_END = """	require.NoError(t, mock.ExpectationsWereMet(),
		"★ 账本必须记 completed + diff_count=1")
}"""


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
    for p in (TARGET, TEST):
        if not os.path.exists(p):
            print("目标文件不存在：%s" % p, file=sys.stderr)
            return 2

    original = snap(TARGET)
    original_test = snap(TEST)
    base_md5 = md5(original)
    base_test_md5 = md5(original_test)
    print("目标：%s" % TARGET_REL)
    print("基线 md5：%s" % base_md5)
    print("")

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
           "rc=0，两条断言全绿", True)

    def restore():
        with open(TARGET, "wb") as f:
            f.write(original)
        with open(TEST, "wb") as f:
            f.write(original_test)
        return md5(snap(TARGET)) == base_md5 and md5(snap(TEST)) == base_test_md5

    try:
        # ── M01：回退成 warn + continue ⇒ 正向断言必须转红 ─────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(replace_once(text, M01_NEEDLE, M01_REPL))

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestReconciliationWorker_DiffWriteFailureMarksRunFailed" in failed
            and "TestReconciliationWorker_DiffWriteSuccessStillCompletes" not in failed
        )
        record(
            "M01 回退成 warn+continue",
            "仍能编译；★ 且**只有**「DiffWriteFailureMarksRunFailed」转红"
            "（★ 反向守卫那条必须照常绿 ⇒ 证明不是「全都红」）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M01 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M02：失败但 error 列写空串 ⇒ 正向断言必须转红 ─────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        text = replace_once(text, M02_ANCHOR, M02_ANCHOR_REPL)
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(text)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestReconciliationWorker_DiffWriteFailureMarksRunFailed" in failed
        )
        record(
            "M02 保留「失败」但 error 列写空串",
            "仍能编译；★ 「DiffWriteFailureMarksRunFailed」转红"
            "（★ ⇒ 证明「error 列必须非空」有牙齿 ⇒ ⇒ 用 AnyArg 代替是**不够的**）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M02 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M03：让这一轮**永远失败** ⇒ 反向守卫必须转红 ──────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(replace_once(text, M03_NEEDLE, M03_REPL))

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestReconciliationWorker_DiffWriteSuccessStillCompletes" in failed
        )
        record(
            "M03 让这一轮永远失败（写入失败的条件改成恒真）",
            "仍能编译；★ **只有**「DiffWriteSuccessStillCompletes」（★ 反向守卫）转红"
            "⇒ ⇒ 证明正向断言**单独**抓不住「永远失败」这种退化 ⇒ ⇒ 反向守卫是必需的",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M03 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M04：M03 的变异 + **删掉**反向守卫 ⇒ 全部转绿 ─────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(replace_once(text, M03_NEEDLE, M03_REPL))
        with open(TEST, encoding="utf-8") as f:
            ttext = f.read()
        i = ttext.find(M03_NEEDLE_START)
        if i < 0:
            print("M04 锚点（反向守卫测试的起点）没找到 ⇒ 中止", file=sys.stderr)
            return 1
        j = ttext.find(M03_NEEDLE_END, i)
        if j < 0:
            print("M04 锚点（反向守卫测试的终点）没找到 ⇒ 中止", file=sys.stderr)
            return 1
        ttext = ttext[:i] + ttext[j + len(M03_NEEDLE_END):]
        with open(TEST, "w", encoding="utf-8") as f:
            f.write(ttext)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = ok_build and rc == 0 and not failed
        record(
            "M04 掏空反向守卫 + M03 的「永远失败」变异",
            "仍能编译；★ **全部测试转绿**（★ ⇒ ⇒ 证明反向守卫是**必需**的："
            "没有它，「把这一轮改成永远失败」也能满足正向断言 ⇒ ★★ 功能被改没了而门全绿）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M04 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

    finally:
        # ── 纪律 ①：还原必须按原路径做 md5 逐字校验 ────────────
        with open(TARGET, "wb") as f:
            f.write(original)
        with open(TEST, "wb") as f:
            f.write(original_test)
        now = md5(snap(TARGET))
        now_test = md5(snap(TEST))
        if now != base_md5 or now_test != base_test_md5:
            print("★ 最终还原校验失败：%s != %s / %s != %s"
                  % (now, base_md5, now_test, base_test_md5), file=sys.stderr)
            return 1

    print("")
    passed = sum(1 for r in LEDGER if r["ok"])
    print("变异台账：%d/%d 通过" % (passed, len(LEDGER)))
    for r in LEDGER:
        print("  %s %s" % ("OK " if r["ok"] else "FAIL", r["arm"]))
    print("还原校验：md5 %s / %s（与基线一致）" % (base_md5, base_test_md5))
    return 0 if passed == len(LEDGER) else 1


if __name__ == "__main__":
    sys.exit(main())
