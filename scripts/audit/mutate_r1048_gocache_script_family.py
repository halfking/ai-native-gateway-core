#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1048「B 级脚本族 ⇒ 必须走专用 GOCACHE」的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1048_gocache_script_family.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 这个台要验的是什么（★ r1047 轮留给本轮的那个缺口）★★

★★ r1047 轮的第二面（★ `TestMutationHarnessScriptsUseDedicatedGoCache`）只覆盖
★★ 【A 级】脚本（★ 改树 ⇒ 跑 go test ⇒ 还原）⇒ ⇒ 而【B 级】那一族（★ 审计 / 部署 /
★★ 压测）★ r1047 轮**诚实地**写成「**明确的漏判面**」⇒ ⇒ ⇒ 本台是那个缺口的兑现。

★★ 本台比 r1047 那台多了一样东西：★★
★★ ★★ **「精准命中」**：★★ ★★ 每个臂都**指名**它必须**只**让哪一条判据转红
★★ ★★ ⇒ ⇒ ★★ ⇒ 其余判据必须**照样绿** ⇒ ⇒ ★★ ⇒ ★★ ⇒ **这样才能证明
★★ ★★ ⇒ ★★ 那条判据不是被别的判据「顺带」带红的** ⇒ ⇒ ★★ ⇒ **判据之间是独立的**。

★★ 纪律（★ 与 r1046 / r1047 两台同构）★★

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ③ ★★★ **逐条判定【是哪一条断言红了】** ⇒ ★ 只看 rc 不算数
★      （★ rc=1 可能来自任何一条 ⇒ ⇒ ★★ 而「**只红该红的那条**」才是强证据）；
★   ④ 环境指纹对拍；
★   ⑤ ★★★ **「没牙」要先过【惰性变异】筛查**（★ B00：★ 先跑一遍未变异的基线
★      ⇒ ⇒ 确认它本来就绿 ⇒ ⇒ 否则「变异后红」可能只是【基线本来就红】）。

★★ 本台的 6 个臂（★ 每个臂对应一条「断言没牙齿」的可能）★★

★   B00 ★★★ **惰性变异筛查**：★ 先跑一遍【未变异】的基线 ⇒ 确认它本来就绿。
★   M08 ★★★ 把一个 B 级脚本的【前导块】整段删掉（★ ⇒ 隔离没了）
★        ⇒ 期望：**只**红 `TestScriptFamilyCompileSitesUseDedicatedGoCache`。
★   M09 ★★★ 把前导块里的 `gocache_use script` 换成
★        `export GOCACHE="$HOME/.cache/go-build"`（★ ⇒ 指向**用户的全局缓存**）
★        ⇒ 期望：**只**红 `TestScriptFamilyHasNoUserGlobalGoCacheOverride`
★        ⇒ ⇒ ★★ 注意它**不会**让「覆盖面」那条红（★ 因为文件里**仍然**有
★        ⇒ ⇒ ★★ `. …/gocache.sh` 的字样 ⇒ ⇒ ★★ ⇒ 「有没有专用证据」判据是**绿的**
★        ⇒ ⇒ ★★ ⇒ **这正是「两处都有」比「只有一处」更刁的地方**）。
★   M10 ★★★ 把前导块换成【条件式】（★ `GOCACHE="${GOCACHE:-…}"`）
★        ⇒ 期望：**只**红 `TestScriptFamilyCompileSitesUseDedicatedGoCache`
★        ⇒ ⇒ ★★ ★★ 而它红的是「**只有条件式**」那一支（★ 不是「压根没指向」那一支）
★        ⇒ ⇒ ★★ ⇒ ★★ ⇒ **这证明门能分清「没设」与「设了但会回落」**。
★   M11 ★★★ 把 `env -i` 那个脚本 env 数组里的 GOCACHE 删掉
★        ⇒ 期望：**只**红 `TestScriptFamilyIsolationSurvivesEnvDashI`
★        ⇒ ⇒ ★★ ⇒ ★★ **这是本轮「逐个读懂脚本」读出来的那一处**。
★   M12 ★★★ 把 GOMODCACHE 指向 `.build-local/` 下
★        ⇒ 期望：**只**红 `TestGomodcacheMustNotLiveUnderBuildLocal`。
★   M13 ★★★ **掏空新门自己的断言**（★ 把 `t.Fatalf` 改成 `t.Logf`）
★        ⇒ 期望：**全部转绿**
★        ⇒ ⇒ ⇒ 证明这五条断言**合起来才是必需的**（★ 没有它们，★ 「把隔离改没了」
★        ⇒ ⇒ ⇒ 也能全绿 ⇒ ⇒ ⇒ **功能被改没了而门全绿**）。
★   M14 ★★★ **r1049 审计轮加的臂**：★ 在一个**已经合格**的 Python 脚本里
★        **额外**插一处【只算路径】的 GOCACHE 赋值
★        （★ `os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")`）
★        ⇒ 期望：**只**红 `TestScriptFamilyNeverSetsGoCacheFromDirFor`
★        ⇒ ⇒ ★★ ⇒ **判据①（文件级）照样绿**（★ 因为那个文件本来就有合格的
★        ⇒ ⇒ ★★ `gocache.apply_to_environ(`）⇒ ⇒ ★★ ⇒ **判据⑥ 是唯一能抓住
★        ⇒ ⇒ ★★ 它的判据** ⇒ ⇒ ★★ ⇒ 这一臂就是判据⑥「不可省」的**证据**。
"""

import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

# ── ★★ 目标清单（★ 存在性是【开跑前的硬前置】★★）──
#
# ★ ★★ **为什么「存在性」是硬前置**：★★ ★★ 变异台的「目标清单」是【对源码结构的假设】
# ★ ★★ ⇒ ⇒ ★★ 源码一旦变了（★ 文件被删 / 被改名）⇒ ⇒ ★★ ⇒ 那个假设就**过期**了
# ★ ★★ ⇒ ⇒ ★★ ⇒ 而**过期假设的失败形态是「静默的」**：★ 若还按老清单去改一个
# ★ ★★ **已不存在的文件**⇒ ⇒ ★★ ⇒ 写回时会**新建**它 ⇒ ⇒ ★★ ⇒ 门里那半边**凭空复活**
# ★ ★★ ⇒ ⇒ ★★ ⇒ 而【原路径的 md5 还原校验】**照样通过** ⇒ ⇒ ★★ ⇒ **rc=0 也可能是错的**
# ★ ★★ ⇒ ⇒ ★★ ⇒ 所以：★ 目标不存在 ⇒ ⇒ **立刻 rc=2 中止** ⇒ ⇒ 绝不「新建」它。
GUARD_REL = "scripts/ursmcheck/gocache_script_family_guard_test.go"
M08_REL = "scripts/audit/verify-outbox-reaper.sh"
M09_REL = "scripts/audit/verify-outbox-reaper.sh"
M10_REL = "scripts/audit/verify-outbox-reaper.sh"
M11_REL = "scripts/audit/measure-factsource-cost-matrix.sh"
M12_REL = "scripts/audit/measure-factsource-cost-matrix.sh"
# ★★ r1049 审计轮加的 M14 目标（★ 一个 Python 形态的变异台）★★
M14_REL = "scripts/audit/mutate_14385_gate.py"
ALL_TARGETS = tuple(
    os.path.join(ROOT, r)
    for r in (GUARD_REL, M08_REL, M11_REL, M14_REL)
)

PKG = "./scripts/ursmcheck/"

# ★★ 只跑【本轮新增的那五条判据】⇒ ⇒ ★★ 跑整包要 686~1150s（★ 本机实测）
# ★★ ⇒ ⇒ ★★ 而本台有 6 个臂 ⇒ ⇒ ★★ ⇒ 全跑要 1 小时+ ⇒ ⇒ ★★ ⇒ 聚焦到本轮的判据。
TESTS = (
    "TestScriptFamilyCompileSitesUseDedicatedGoCache"
    "|TestScriptFamilyIsolationSurvivesEnvDashI"
    "|TestScriptFamilyHasNoUserGlobalGoCacheOverride"
    "|TestGomodcacheMustNotLiveUnderBuildLocal"
    "|TestScriptFamilyExclusionRegistryIsExact"
    "|TestScriptFamilyNeverSetsGoCacheFromDirFor"
    "|TestScriptFamilyCompileScopeIgnoresDocstrings"
    "|TestScriptFamilyUserGlobalOverrideDetectsBothForms"
    "|TestScriptFamilyDirForJudgeDistinguishesPayloadFromCode"
    "|TestGoCacheDedicatedFormsAreRecognized"
    "|TestMutationHarnessScriptsUseDedicatedGoCache"
)

# ── ★★ 变异锚点 ──

# ★★ 前导块的起止（★ ★★ 形态必须与 `scripts/lib/gocache.sh` 头注释里写的一致 ★★）。
PRELUDE_START = "# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go"
PRELUDE_END = "  gocache_use script\nfi\n"

# M08：★ 整段删掉前导块。
M08_NEEDLE = None  # ★ 用正则删（★ 见 main 里的 replace_block）
M08_RE = re.compile(
    re.escape(PRELUDE_START) + r".*?" + re.escape(PRELUDE_END),
    re.S,
)

# M09：★ 把 `gocache_use script` 换成「指向用户全局缓存」。
M09_NEEDLE = "  gocache_use script\n"
M09_REPL = '  export GOCACHE="$HOME/.cache/go-build"  # MUTATION M09: 回落用用户的全局缓存\n'

# M10：★ 把整段前导块换成【条件式】。
M10_REPL = (
    'export GOCACHE="${GOCACHE:-$("$__gocache_dir/scripts/lib/gocache.sh" script)}"'
    "  # MUTATION M10: 条件式 ⇒ 用户环境里已设 GOCACHE ⇒ 悄悄回落用全局缓存\n"
)

# M11：★ 把 `env -i` 那个 env 数组里的 GOCACHE 删掉。
M11_NEEDLE = '          GOCACHE="$GOCACHE" GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"\n'
M11_REPL = '          GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"\n'

# M12：★ 把 GOMODCACHE 指向 `.build-local/` 下。
M12_NEEDLE = 'GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"'
M12_REPL = 'GOMODCACHE="${GOMODCACHE:-$HOME/.build-local/gomodcache}"'

# ★★ M14（★ r1049 审计轮加的臂）：★★ 在一个**已合格**的 Python 脚本里，
# ★★ **额外**插一处【只算路径】的 GOCACHE 赋值 ⇒ ⇒ ★★ ⇒
# ★★ ⇒ ★★ **判据①（文件级）照样绿**（★ 因为那个文件本来就有合格的
# ★★ `gocache.apply_to_environ(`）⇒ ⇒ ★★ ⇒ **判据⑥ 是唯一能抓住它的判据**
# ★★ ⇒ ⇒ ★★ ⇒ 这一臂就是判据⑥「不可省」的**证据**。
M14_NEEDLE = 'gocache.apply_to_environ("mutate")\n'
M14_REPL = (
    'gocache.apply_to_environ("mutate")\n'
    'os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")'
    '  # MUTATION M14: 只算路径 ⇒ 不建目录不查可写 ⇒ 不失败安全\n'
)

# M13：★ 掏空新门自己的断言。
M13_NEEDLE = "t.Fatalf("
M13_REPL = "t.Logf("

LEDGER = []


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
    """给子进程准备环境（★ 与 r1046 / r1047 两台同款约定）。"""
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + env.get("PATH", "")
    env["GOCACHE"] = os.path.join(ROOT, ".build-local", ".gocache-mutate")
    return env


def run_tests():
    proc = subprocess.run(
        ["go", "test", PKG, "-run", TESTS, "-count=1", "-v"],
        cwd=ROOT,
        env=go_env(),
        capture_output=True,
        text=True,
        timeout=1800,
    )
    return proc.returncode, proc.stdout + proc.stderr


def failed_tests(output):
    """返回失败测试名列表（★ 逐条指名，★ 不只看 rc）。"""
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
        timeout=1800,
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


def record(arm, expect, got, ok):
    LEDGER.append({"arm": arm, "expect": expect, "got": got, "ok": ok})
    print("[%s] %s" % ("OK " if ok else "FAIL", arm))
    print("       期望：%s" % expect)
    print("       实测：%s" % got)


def main() -> int:
    # ★★ 纪律：★ 目标文件存在性是开跑前的硬前置 ⇒ ⇒ 过期假设不许静默过去。
    for path in ALL_TARGETS:
        if not os.path.exists(path):
            print("目标文件不存在：%s（★ 源码结构变了？★ 变异台的目标清单该收敛了）" % path,
                  file=sys.stderr)
            return 2

    originals = {p: snap(p) for p in ALL_TARGETS}
    base_md5 = {p: md5(originals[p]) for p in ALL_TARGETS}
    for p in ALL_TARGETS:
        print("目标：%s（基线 md5 %s）" % (os.path.relpath(p, ROOT), base_md5[p]))
    print("")

    def read(path):
        with open(path, "r", encoding="utf-8") as f:
            return f.read()

    def write(path, text):
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)

    def restore():
        for p in ALL_TARGETS:
            write(p, originals[p].decode("utf-8"))
            now = md5(snap(p))
            if now != base_md5[p]:
                print("★ 还原后 md5 不一致：%s（★ %s ≠ %s）" % (p, now, base_md5[p]),
                      file=sys.stderr)
                return False
        return True

    def arm(name, target_rel, expect_red, mutate):
        """跑一个臂：★ 变异 ⇒ ⇒ 编译检查 ⇒ ⇒ 跑门 ⇒ ⇒ **逐条**核对红的是哪几条
        ⇒ ⇒ ⇒ 还原 ⇒ ⇒ md5 校验。"""
        path = os.path.join(ROOT, target_rel)
        text = read(path)
        new_text = mutate(text)
        if new_text == text:
            record(name, "变异锚点命中（★ 而它没命中 ⇒ ★ 目标清单/锚点过期）",
                   "★ 锚点没命中 ⇒ ★ 什么都没改", False)
            restore()
            return False
        write(path, new_text)

        ok_compile, comp_out = compile_ok()
        if not ok_compile:
            record(name, "编译仍然通过（★ 编译红 ≠ 断言红 ⇒ 不算数）",
                   "★ 编译红：%s" % comp_out.strip()[:300], False)
            restore()
            return False

        rc, out = run_tests()
        reds = failed_tests(out)
        got = "rc=%s，红：%s" % (rc, ", ".join(reds) if reds else "无")
        want = set(expect_red)
        have = set(reds)
        # ★★★ **「精准命中」**：★★ 红的**集合**必须与期望**逐条相等** ⇒ ⇒ ★★
        # ★★★ 「其余照常绿」也是证据的一部分（★ 否则一条判据坏了会把别的也带红，
        # ★★★ ⇒ ★★★ 而我们就分不清是谁在起作用）。
        ok = (want == have)
        record(name,
               "只红：%s（★ 其余照常绿）" % (", ".join(sorted(want)) if want else "无（★ 期望全绿）"),
               got, ok)
        restore()
        return ok

    # ── B00：★ 惰性变异筛查（★ 基线本来就绿）──
    rc, out = run_tests()
    reds = failed_tests(out)
    record("B00 基线（惰性变异筛查）", "rc=0 且 0 条测试失败（★ 基线本来就绿）",
           "rc=%s，红：%s" % (rc, ", ".join(reds) if reds else "无"), rc == 0 and not reds)

    # ── M08：★ 删掉前导块 ⇒ 「覆盖面」那条红 ──
    def m08(text):
        new, n = M08_RE.subn("", text, count=1)
        return new if n else text

    arm("M08 删掉 B 级脚本的前导块（★ 隔离没了）", M08_REL,
        ["TestScriptFamilyCompileSitesUseDedicatedGoCache"], m08)

    # ── M09：★ 把隔离改成指向用户全局缓存 ⇒ 「不许有指向仓外用户目录的 GOCACHE」那条红 ──
    def m09(text):
        return text.replace(M09_NEEDLE, M09_REPL, 1)

    arm("M09 把隔离改成 GOCACHE=$HOME/.cache/go-build（★ 用户全局缓存）", M09_REL,
        ["TestScriptFamilyHasNoUserGlobalGoCacheOverride"], m09)

    # ── M10：★ 把隔离改成条件式 ⇒ 「覆盖面」那条的「只有条件式」那一支红 ──
    def m10(text):
        new, n = M08_RE.subn(M10_REPL, text, count=1)
        return new if n else text

    arm("M10 把隔离改成条件式 GOCACHE=${GOCACHE:-…}（★ 会悄悄回落）", M10_REL,
        ["TestScriptFamilyCompileSitesUseDedicatedGoCache"], m10)

    # ── M11：★ 删掉 env -i 那个 env 数组里的 GOCACHE ⇒ 「env -i 那一半」红 ──
    def m11(text):
        return text.replace(M11_NEEDLE, M11_REPL, 1)

    arm("M11 删掉 env -i 那个 env 数组里的 GOCACHE（★ ⇒ 顶部 export 被抹掉）", M11_REL,
        ["TestScriptFamilyIsolationSurvivesEnvDashI"], m11)

    # ── M12：★ 把 GOMODCACHE 指向 .build-local/ ⇒ 「模块缓存不许进 .build-local」红 ──
    def m12(text):
        return text.replace(M12_NEEDLE, M12_REPL, 1)

    arm("M12 把 GOMODCACHE 指向 .build-local/ 下（★ ⇒ git clean 会删掉模块缓存）", M12_REL,
        ["TestGomodcacheMustNotLiveUnderBuildLocal"], m12)

    # ── M14：★ 在【已合格】的脚本里**额外**插一处【只算路径】的赋值 ──
    # ★★ ⇒ ⇒ ★★ **判据①（文件级）照样绿** ⇒ ⇒ ★★ ⇒ **只有判据⑥ 能抓住它**
    # ★★ ⇒ ⇒ ★★ ⇒ 这一臂就是判据⑥「不可省」的**证据**。
    def m14(text):
        return text.replace(M14_NEEDLE, M14_REPL, 1)

    arm("M14 在合格脚本里额外插 dir_for 赋值（★ ⇒ 只有判据⑥ 红）", M14_REL,
        ["TestScriptFamilyNeverSetsGoCacheFromDirFor"], m14)

    # ── M13：★ 掏空新门自己的断言 ⇒ ★ 全部转绿 ⇒ ⇒ 证明几条断言合起来才是必需的 ──
    def m13(text):
        return text.replace(M13_NEEDLE, M13_REPL)

    arm("M13 掏空新门自己的断言（★ ⇒ 期望全部转绿）", GUARD_REL, [], m13)

    # ── 收尾：★ 再核一次基线（★ 证明所有臂都还原干净了）──
    rc, out = run_tests()
    reds = failed_tests(out)
    record("B99 收尾复核（★ 所有臂都还原干净了吗）", "rc=0 且 0 条测试失败",
           "rc=%s，红：%s" % (rc, ", ".join(reds) if reds else "无"), rc == 0 and not reds)

    failed = [x for x in LEDGER if not x["ok"]]
    print("")
    print("=== 台账：%d 条，%d 条符合预期 ===" % (len(LEDGER), len(LEDGER) - len(failed)))
    for x in failed:
        print("  ★ %s ⇒ 期望 %s / 实测 %s" % (x["arm"], x["expect"], x["got"]))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
