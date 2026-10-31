#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1047「会编译 Go 的子进程 ⇒ 必须走【专用 GOCACHE】」的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1047_gocache_isolation_rollout.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 背景（★ 实测，2026-10-28）：

★★ r1046 轮把**掏空门**的子构建隔离到专用 GOCACHE ⇒ ⇒ 但那**只是第一处**。
★★ 本轮先做了一次**同族普查**（★ 全仓「会 exec go 工具链」的地方）⇒ ⇒ 实测
★★ 仓内共有 **9 处** Go 工具链子进程点 ⇒ ⇒ 其中 **8 处**当时仍写用户的全局缓存。
★★ ⇒ ⇒ ★★ 而那些产物随树变化而失效 ⇒ ⇒ **永远不会再被命中** ⇒ ⇒ 纯垃圾
★★ ⇒ ⇒ ★★ 实测那个全局缓存一度到 **72 GB** ⇒ ⇒ 磁盘占满 ⇒ ⇒ 全量
★★ `go test ./...` **编译阶段**就红 ⇒ ⇒ ★★ **红的原因不指向真正的原因**。

★★ 本轮做两件事：
★★ ① **推广**：★ 把另外 8 处也指向【专用 GOCACHE】（★ 共享包 `internal/gocache`）；
★★ ② **建门**：★ 新门 `scripts/ursmcheck/gocache_isolation_guard_test.go`
★★    ⇒ ⇒ 扫出全仓所有 Go 工具链子进程点 ⇒ ⇒ 断言它们的 env 指向专用目录。

★★ 本台覆盖的 5 个臂（★ 每个臂对应一条「断言没牙齿」的可能）：

★   B00 ★★★ **惰性变异筛查**：★ 先跑一遍【未变异】的基线 ⇒ 确认它本来就绿。
★   M01 ★★★ 把 `sql/schema` 那道门的两处隔离**删掉**（★ ⇒ 子进程回落用全局缓存）
★        ⇒ 期望：新门**转红**且**只报这 2 处** ⇒ ⇒ 证明新门咬得住这个形态。
★   M02 ★★★ 把 `plugin-runtime` 那两处的隔离**换回 `os.Environ()`**
★        ⇒ 期望：新门**转红**且**只报 1 处** ⇒ ⇒ 证明「同包里的另一处仍隔离」
★        ⇒ ⇒ **不会连坐**（★ ⇒ ⇒ 证明它报的是【具体那处】不是【整个包】）。
★   M04 ★★★ **删掉一个 shell 变异台的 GOCACHE 隔离** ⇒ ⇒ 期望：【脚本家族】那道门转红
★        ⇒ ⇒ ★★ 证明第二面**确实有牙齿**（★ 否则 66 个脚本那一族完全裸奔）。
★   M03 ★★★ **掏空新门自己的断言**（★ `t.Fatalf` ⇒ `t.Logf`）★ **再叠加 M01**
★        ⇒ 期望：**全部转绿**
★        ⇒ ⇒ ⇒ 证明新门的断言**合起来才是必需的**（★ 没有它，★ 「把隔离改没了」
★        ⇒ ⇒ ⇒ 也能全绿 ⇒ ⇒ ⇒ **功能被改没了而门全绿**）。

★★ 纪律（★ 与 mutate_r1046_hollowout_gocache_isolation.py 同构）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ④ 每个变异都必须自带【效果探针】；
★★ ⑤ ★★★ **「没牙」要先过【惰性变异】筛查**；
★★ ⑥ ★★★ 逐条判定【是哪一条断言红了】⇒ 只看 rc 不算数（★ rc=1 可能来自任何一条）。
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# ★★ 复用 r1046 那台的**基础工具**（★ ⇒ ⇒ 目录约定、还原纪律、md5 校验
# ★★ 只有一份 ⇒ ⇒ 不重复实现 ⇒ ⇒ 改一处就够）。
import mutate_r1046_hollowout_gocache_isolation as base  # noqa: E402

# ★ 本台要跑的是【新门】⇒ ⇒ 覆盖掉基座里的 TESTS 口径。
GATE_TEST = "TestGoToolchainSubprocessesUseDedicatedGoCache"
# ★★ 第二面（★ 脚本家族）那道门也归本台管（★ 它和第一面是同一个问题）。
SCRIPT_GATE_TEST = "TestMutationHarnessScriptsUseDedicatedGoCache"
base.TESTS = GATE_TEST + "|" + SCRIPT_GATE_TEST


# ★★ r1047 轮（★ 2026-10-28）：★ 本台**自己**把 `GOCACHE` 指向【专用目录】⇒ ⇒
# ★★ ★★ 原来它只是**继承**基座的 `go_env()` ⇒ ⇒ ★★ 「约定」就有两份（★ 一份在
# ★★ 基座、一份在本台）⇒ ⇒ ★★ ★★ 而**文件级**的判据（★ 门
# ★★ `TestMutationHarnessScriptsUseDedicatedGoCache`）**看不见**继承 ⇒ ⇒ ★★ ⇒
# ★★ ⇒ **继承**是「约定有两份」的形态 ⇒ ⇒ ★★ ⇒ **每台自己写出来**。
def _go_env():
    env = dict(os.environ)
    env["PATH"] = base.GO_BIN + os.pathsep + env.get("PATH", "")
    env["GOCACHE"] = os.path.join(base.ROOT, ".build-local", ".gocache-mutate")
    return env


base.go_env = _go_env

TARGET_REL = "scripts/ursmcheck/gocache_isolation_guard_test.go"
TARGET = os.path.join(base.ROOT, TARGET_REL)

SQL_REL = "sql/schema/integration_gate_test.go"
SQL = os.path.join(base.ROOT, "sql", "schema", "integration_gate_test.go")

PLUGIN_REL = "plugin-runtime/supervisor_test.go"
PLUGIN = os.path.join(base.ROOT, PLUGIN_REL)

# ★★ 第三个目标：★ 一个 shell 变异台（★ 用来验【脚本家族】那道门）。
SHELL_REL = "scripts/.mutate-840-throttle.sh"
SHELL = os.path.join(base.ROOT, SHELL_REL)

ALL_TARGETS = (TARGET, SQL, PLUGIN, SHELL)

# ── M01 锚点：把 sql/schema 那道门的隔离删掉 ────────────────────────────
M01_NEEDLES = (
    '\tlistCmd.Env = gocache.Env(root, "integration-vet")\n',
    '\tcmd.Env = gocache.Env(root, "integration-vet")\n',
)

# ── M02 锚点：把 plugin-runtime 的隔离换回裸 os.Environ() ───────────────
M02_NEEDLE = 'gocache.Env(gocacheRoot(t), "plugin-runtime")'
M02_REPL = 'os.Environ()'

# ── M03 锚点：掏空新门自己的断言 ────────────────────────────────────────
# ★★★ **必须用【块注释】**：★ 行注释 `//` 会吃掉该行余下的内容 ⇒ ⇒
# ★★★ **编译红** ⇒ ⇒ 按纪律「编译红 ≠ 断言红 ⇒ 不算数」。
M03_NEEDLE = "t.Fatalf("
M03_REPL = "t.Logf(/* MUTATION M03: 断言被掏空 */"

# ── M04 锚点（★ r1047 轮第二面）：★ 把一个 shell 变异台的 GOCACHE 隔离删掉 ──
#
# ★★★ **为什么这一臂不可省**：★★ 第一面（★ Go 文件）那道门**看不见 shell / Python**
# ★★★ ⇒ ⇒ ★★★ 「脚本家族」那一族（★ ★★ 70 个脚本、★ 其中 66 个调 go 工具链）
# ★★★ ⇒ ⇒ ★★★ **改树 ⇒ 跑 go test ⇒ 还原** ⇒ ⇒ ★★★ **每次跑都产生一批永不复用的
# ★★★ 缓存条目** ⇒ ⇒ ★★★ ⇒ **没有第二面 ⇒ ⇒ 这一族完全裸奔**。
M04_NEEDLE = 'export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"'
M04_REPL = 'export GOCACHE="$HOME/.cache/go-build"  # MUTATION M04: 回落用用户的全局缓存' 


def replace_first(text, needle, repl, expect_count):
    """只替换**第一处**（★ ★★ 但必须先断言总数 == expect_count）。

    ★★ **为什么 M02 需要它**：★ `plugin-runtime/supervisor_test.go` 里有【两处】
    ★★ 同样的隔离调用 ⇒ ⇒ ★★ M02 要证明的是【新门报的是【具体那处】而不是
    ★★ 【整个包】】⇒ ⇒ ★★ ⇒ 所以只改**一处** ⇒ ⇒ ★★ ⇒ 另一处仍隔离。
    """
    n = text.count(needle)
    if n != expect_count:
        raise AssertionError("锚点命中 %d 次（期望恰好 %d 次）⇒ 变异没有落在预期的那几处" % (n, expect_count))
    return text.replace(needle, repl, 1)


def main() -> int:
    for t in ALL_TARGETS:
        if not os.path.exists(t):
            print("目标文件不存在：%s" % t, file=sys.stderr)
            return 2

    originals = {t: base.snap(t) for t in ALL_TARGETS}
    base_md5 = {t: base.md5(d) for t, d in originals.items()}
    for t in ALL_TARGETS:
        print("目标：%s（基线 md5 %s）" % (os.path.relpath(t, base.ROOT), base_md5[t]))
    print("")

    def read(path):
        with open(path, encoding="utf-8") as f:
            return f.read()

    def write(path, text):
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)

    def restore():
        ok = True
        for path, data in originals.items():
            with open(path, "wb") as f:
                f.write(data)
            now = base.md5(base.snap(path))
            if now != base_md5[path]:
                ok = False
                print("★ 还原校验失败：%s（%s != %s）" % (
                    os.path.relpath(path, base.ROOT), now, base_md5[path]), file=sys.stderr)
        return ok

    # ── 纪律 ⑤：惰性变异筛查 —— 先确认基线本来就绿 ──────────────
    rc, out = base.run_tests()
    if rc != 0 or base.failed_tests(out):
        base.record(
            "B00 基线（惰性变异筛查）",
            "rc=0 且 0 条测试失败（★ 基线本来就绿 ⇒ 否则「变异后红」可能只是基线本来就红）",
            "rc=%s，红：%s" % (rc, base.failed_tests(out) or "无"),
            False,
        )
        return 1
    base.record("B00 基线（惰性变异筛查）", "rc=0 且 0 条测试失败", "rc=0，新门绿", True)

    try:
        # ── M01：sql/schema 的隔离被删掉 ⇒ 新门必须报那 2 处 ──────
        text = read(SQL)
        for needle in M01_NEEDLES:
            text = base.replace_once(text, needle, "")
        write(SQL, text)

        ok_build, build_out = base.compile_ok()
        rc, out = base.run_tests()
        failed = base.failed_tests(out)
        # ★★★ 逐条判定【是哪一条断言红了】+ 【报的是不是那 2 处】。
        detail = out
        ok = (
            ok_build
            and rc != 0
            and GATE_TEST in failed
            # ★★ 用「出现次数」而不是【行号】⇒ ⇒ 变异会挪行 ⇒ ⇒ 行号不可靠
            # ★★ ⇒ ⇒ **只判「那 2 处」这个集合**（★ 行号在实测里是 821 / 837）。
            and detail.count("integration_gate_test.go:") == 2
            and "plugin-runtime/supervisor_test.go" not in detail
        )
        base.record(
            "M01 删掉 sql/schema 那道门的两处隔离",
            "仍能编译；★ 新门转红且**只报 integration_gate_test.go 的那 2 处**"
            "（★ ⇒ 证明它咬得住「子进程回落用全局缓存」这个形态）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M01 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M02：plugin-runtime 的隔离被换回 os.Environ() ⇒ 只报那 1 处 ─
        text = read(PLUGIN)
        text = replace_first(text, M02_NEEDLE, M02_REPL, 2)
        write(PLUGIN, text)

        ok_build, build_out = base.compile_ok()
        rc, out = base.run_tests()
        failed = base.failed_tests(out)
        detail = out
        ok = (
            ok_build
            and rc != 0
            and GATE_TEST in failed
            and detail.count("plugin-runtime/supervisor_test.go:") == 1
            and "integration_gate_test.go" not in detail
        )
        base.record(
            "M02 把 plugin-runtime 的一处隔离换回 os.Environ()",
            "仍能编译；★ 新门转红且**只报 plugin-runtime/supervisor_test.go:120**"
            "（★ ⇒ 同包里的另一处仍隔离 ⇒ ⇒ 证明它报的是【具体那处】不是【整个包】）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M02 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M03：掏空新门的断言 + M01 ⇒ 全部转绿 ───────────────
        text = read(TARGET)
        n = text.count(M03_NEEDLE)
        if n < 1:
            print("M03 锚点（t.Fatalf）没找到 ⇒ 中止", file=sys.stderr)
            return 1
        text = text.replace(M03_NEEDLE, M03_REPL)
        write(TARGET, text)

        sql_text = read(SQL)
        for needle in M01_NEEDLES:
            sql_text = base.replace_once(sql_text, needle, "")
        write(SQL, sql_text)

        ok_build, build_out = base.compile_ok()
        rc, out = base.run_tests()
        failed = base.failed_tests(out)
        ok = ok_build and rc == 0 and not failed
        base.record(
            "M03 掏空新门自己的断言 + M01 的「删掉隔离」变异",
            "仍能编译；★ **全部转绿**（★ ⇒ ⇒ 证明新门的断言**合起来才是必需的**："
            "没有它，★ 「把隔离改没了」也能全绿 ⇒ ★★ 功能被改没了而门全绿）",
            "编译=%s，rc=%s，红：%s%s（★ 掏空了 %d 处 t.Fatalf）" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数", n),
            ok,
        )
        if not restore():
            print("M03 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M04：删掉一个 shell 变异台的 GOCACHE 隔离 ⇒ 脚本家族那道门必须转红 ──
        text = read(SHELL)
        text = base.replace_once(text, M04_NEEDLE, M04_REPL)
        write(SHELL, text)

        ok_build, build_out = base.compile_ok()
        rc, out = base.run_tests()
        failed = base.failed_tests(out)
        detail = out
        ok = (
            ok_build
            and rc != 0
            and SCRIPT_GATE_TEST in failed
            and GATE_TEST not in failed
            and SHELL_REL in detail
        )
        base.record(
            "M04 删掉一个 shell 变异台的 GOCACHE 隔离",
            "仍能编译；★ **只有**「MutationHarnessScriptsUseDedicatedGoCache」转红"
            "（★ ⇒ 证明「脚本家族」那一面【确实有牙齿】⇒ ⇒ 不是恒绿）",
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
        if not restore():
            return 1

    print("")
    passed = sum(1 for r in base.LEDGER if r["ok"])
    print("变异台账：%d/%d 通过" % (passed, len(base.LEDGER)))
    for r in base.LEDGER:
        print("  %s %s" % ("OK " if r["ok"] else "FAIL", r["arm"]))
    for t in ALL_TARGETS:
        print("还原校验：md5 %s（%s，与基线一致）" % (
            base_md5[t], os.path.relpath(t, base.ROOT)))
    return 0 if passed == len(base.LEDGER) else 1


if __name__ == "__main__":
    sys.exit(main())
