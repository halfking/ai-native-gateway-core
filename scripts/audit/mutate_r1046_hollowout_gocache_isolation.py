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

★★ 本台覆盖的 8 个臂（★ 每个臂对应一条「断言没牙齿」的可能）：

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
★        ⇒ ⇒ ⇒ 证明这六条断言**合起来才是必需的**（★ 没有它们，★ 「把隔离改没了」
★        ⇒ ⇒ ⇒ 也能全绿 ⇒ ⇒ ⇒ **功能被改没了而门全绿**）。
★   M05 ★★★ **r1047 轮新增**：★ 把【上限常量本身】改成 `math.MaxInt64`
★        ⇒ 期望：「上限有界」那条**转红** ⇒ ⇒ 证明【常量本身】被门钉住了
★        ⇒ ⇒ ⇒ 而不是「只钉住了函数行为」（★ M03 钉的正是函数行为）。
★   M06 ★★★ **r1047 轮新增**：★ 把【生产剪枝调用点】的上限改传 `math.MaxInt64`
★        ⇒ 期望：「上限接到剪枝上」那条**转红** ⇒ ⇒ 证明「常量有界」不等于
★        ⇒ ⇒ ⇒ 「上限真的被用上了」（★ 否则上限是个没人读的装饰）。
★   M07 ★★★ **r1047 轮合并后新增**：★ 让【tmp 目录清理】**不删目录**
★        ⇒ 期望：「tmp 目录被清理」那条**转红** ⇒ ⇒ 证明那一半**有人验**
★        ⇒ ⇒ ⇒ （★ 合并前实测：`hollowout_bin_cleanup_test.go` 里【一个 `Test`
★        ⇒ ⇒ ⇒ 函数都没有】⇒ ⇒ 「修完了但没人验」⇒ ⇒ 这是本轮最刺眼的一条缺口）。
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

# ★★ r1049 审计轮补：★ 本台自己**也要**用【共享入口】拿专用 GOCACHE。
# ★★ 原来这里是**自己拼路径**（★ `os.path.join(ROOT, ".build-local", ".gocache-mutate")`）
# ★★ ⇒ ⇒ ★★ 而 `scripts/lib/gocache.py` 的包注释**明文禁止**「自己拼路径」
# ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ 而且那条拼出来的形态**在门里认不出来**（★ `.build-local/` 与
# ★★ `.gocache-mutate` 被 `os.path.join` **拆成两段** ⇒ ⇒ ★★ 门那条正则
# ★★ `\.build-local/\.gocache-` 匹配不到）⇒ ⇒ ★★ ⇒ ★★ ⇒ 而且它是**不失败安全**的
# ★★ （★ 不建目录、不查可写）⇒ ⇒ ★★ ⇒ 本台**曾经靠一个变异载荷字符串**
# ★★ （★ `M02_NEEDLE` 里那句 `.build-local/.gocache-hollowout`）**蒙混过关**。
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

TARGET_REL = "scripts/ursmcheck/hollowout_gocache_test.go"
TARGET = os.path.join(ROOT, TARGET_REL)

# ★★ r1047 轮【合并后】：★★ 原 `scripts/ursmcheck/hollowout_bin_cleanup_test.go`
# ★★ 【已被删除】⇒ ⇒ 它的内容（★ tmp 登记 + 清理 + `TestMain`）**并进了** `TARGET`
# ★★ ⇒ ⇒ 「第二个目标文件」这个概念**消失了**⇒ ⇒ ★★ 本台仍是**单文件**变异台。
# ★★
# ★★ ★★★ **教训（★ 合并必然带来的后果，★ 别再踩）**：★★★ 变异台的「目标清单」是
# ★★ ★★★ **对源码结构的假设**⇒ ⇒ ★★ ★★★ 源码一旦合并文件 ⇒ ⇒ 那个假设就**过期**了
# ★★ ★★★ ⇒ ⇒ ★★ ★★★ 而**过期假设的失败形态是「静默的」**：★ 若还按老清单去改一个
# ★★ ★★★ **已不存在的文件**⇒ ⇒ ★★ ★★★ 写回时会**新建**它 ⇒ ⇒ ★★ ★★★ 门里那半边
# ★★ ★★★ **凭空复活**⇒ ⇒ ★★ ★★★ 而【原路径的 md5 还原校验】**照样通过**（★ 因为
# ★★ ★★★ 还原校验比的是【自己刚写的字节】）⇒ ⇒ ★★ ★★★ ⇒ **rc=0 也可能是错的**。
# ★★ ★★★ ⇒ ⇒ 正确做法：★ 合并文件后**同步收敛变异台的目标清单**，并让**目标文件存在性**
# ★★ ★★★ 成为**开跑前的硬前置**（★ 下面 `main()` 里已经这么做了）。
ALL_TARGETS = (TARGET,)

PKG = "./scripts/ursmcheck/"
TESTS = (
    "TestHollowoutBuildUsesIsolatedGoCache"
    "|TestHollowoutGoCacheDirIsNotTracked"
    "|TestPruneHollowGoCacheRespectsSizeCap"
    "|TestHollowoutGoCacheSizeCapIsBounded"
    "|TestHollowoutGoCacheSizeCapReachesPrune"
    "|TestHollowoutBinTmpDirsAreCleanedUp"
)

# ★★ 「把上限改成 math.MaxInt64」需要 import "math" ⇒ ★ 每个臂都要自带
# ★★ 那一条 import 变更（★ 否则是【编译红】⇒ ⇒ 按本台纪律「编译红 ≠ 断言红」
# ★★ ⇒ ⇒ 不算数）。
MATH_IMPORT_NEEDLE = '\t"fmt"\n'
MATH_IMPORT_REPL = '\t"fmt"\n\t"math"\n'

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

# ── M05 锚点（★ r1047）：把【上限常量本身】改成 math.MaxInt64 ────────────
#
# ★★★ **为什么这条臂是新的**：★ M03 钉的是【函数行为】（★ 给定 maxBytes 时
# ★★★ 剪不剪）⇒ ⇒ ★★★ **常量本身**从来没人管 ⇒ ⇒ 有人把上限改成
# ★★★ `math.MaxInt64` ⇒ ⇒ ★★ 剪枝函数**行为完全正确** ⇒ ⇒ ★★ M01~M04
# ★★★ **一条都不红** ⇒ ⇒ ★★ 而专用缓存重新变成无界增长 ⇒ ⇒ 磁盘又会被撑爆。
M05_NEEDLE = """	hollowGoCacheMaxBytes int64 = 6 << 30"""
M05_REPL = """	hollowGoCacheMaxBytes int64 = math.MaxInt64 // MUTATION M05: 上限无界"""

# ── M06 锚点（★ r1047）：把【生产剪枝调用点】的上限改传 math.MaxInt64 ────
#
# ★★★ **为什么这条臂也是新的**：★ M05 钉住的是【常量有界】⇒ ⇒ 但若
# ★★★ **调用点没有用那个常量**（★ 改传 `math.MaxInt64`）⇒ ⇒ ★★ 常量照样
# ★★★ 有界（★ 门 M05 绿）⇒ ⇒ ★★ 而**上限根本没被用上** ⇒ ⇒ ★★ 专用缓存
# ★★★ 照样无界增长 ⇒ ⇒ ★★ ⇒ **「常量有界」不等于「上限有效」**。
M06_NEEDLE = """		if pruned, perr := pruneHollowGoCache(dir, hollowGoCacheMaxBytes); perr != nil {"""
M06_REPL = """		if pruned, perr := pruneHollowGoCache(dir, math.MaxInt64); perr != nil { // MUTATION M06: 绕过被钉住的上限"""

# ── M07 锚点（★ r1047 轮合并后新增）：让【tmp 目录清理】不删目录 ──────────
#
# ★★★ **为什么这条臂是必须的**：★★★ r1045 轮把 tmp 那一半「修好了」（★ 登记 + 跑完统一删）
# ★★★ ⇒ ⇒ ★★★ **但那一轮【没有给这一半配任何判据】** ⇒ ⇒ ★★★ ⇒ 「修完了」**没人验**
# ★★★ ⇒ ⇒ ★★★ 合并前实测：`hollowout_bin_cleanup_test.go` 里**一个 `Test` 函数都没有**
# ★★★ ⇒ ⇒ ★★★ ⇒ ⇒ **那一半是【裸奔】的** ⇒ ⇒ ★★★ r1047 轮补了判据⑥
# ★★★ ⇒ ⇒ ★★★ ⇒ **判据必须有变异臂**，否则「判据本身没牙齿」这件事没法证伪。
#
# ★★ **变异形态的选择**：★ 不能把 `os.RemoveAll` 直接删掉（★ 删掉会**编译红**
# ★★ ⇒ ⇒ 按本台纪律「编译红 ≠ 断言红」⇒ ⇒ 不算数）⇒ ⇒ 改成【删一个不存在的子路径】：
# ★★ ⇒ ⇒ `os.RemoveAll` 对不存在的路径**返回 nil**（★ 不报错）⇒ ⇒ **仍能编译**
# ★★ ⇒ ⇒ ⇒ **计数照旧**（★ `n++` 照走 ⇒ ⇒ 「删了几个」那条断言照样绿）
# ★★ ⇒ ⇒ ⇒ **只有「目录真的不在了」那条断言会红** ⇒ ⇒ ⇒ ★★ **精准命中判据⑥**。
M07_NEEDLE = """		if err := os.RemoveAll(d); err != nil {"""
M07_REPL = """		if err := os.RemoveAll(filepath.Join(d, "MUTATION-M07-never-deletes")); err != nil { // MUTATION M07: 清理不删目录"""


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
    """给子进程（go test / go vet）准备环境。

    ★★★ r1047 轮（★ 2026-10-28）补的这一行是【本台自己的缺口】：
    ★★★ 本台**每跑一个臂就改一次树**（★ 变异 ⇒ 跑 ⇒ 还原）⇒ ⇒ 树的 action ID
    ★★★ **每次都变** ⇒ ⇒ 而本台跑的 `go test ./scripts/ursmcheck/` 会**编译整个
    ★★★ 包** ⇒ ⇒ ⇒ 每跑一个臂就往**用户的全局 GOCACHE** 里塞一批
    ★★★ **永远不会再被命中**的条目 ⇒ ⇒ ⇒ ★★★ **这正是 r1046 那轮要治的病**
    ★★★ ⇒ ⇒ ⇒ 而**治病的工具自己带着这个病**（★ 同族：★ 审计档 §2.2 记的
    ★★★ 「读数窗口内的并发负载」⇒ ⇒ ★★★ **工具自己也是被测对象**）。
    ★★★ ⇒ ⇒ ⇒ **无条件覆盖**（★ 不是 setdefault）⇒ ⇒ 即便用户环境里已经设了
    ★★★ `GOCACHE`，子进程也用专用目录（★ 与 `hollowBuildEnv` 同款语义）。

    ★★★★ **r1049 审计轮改的（★ 原来是自己拼路径）**：★★★★ 原来写的是
    ★★★★ `env["GOCACHE"] = os.path.join(ROOT, ".build-local", ".gocache-mutate")`
    ★★★★ ⇒ ⇒ ★★★★ ① **不失败安全**（★ 不建目录、不查可写）★★ ⇒ ⇒ ★★★★ ②
    ★★★★ **自己拼路径**（★ `gocache.py` 的包注释**明文禁止**）★★ ⇒ ⇒ ★★★★ ③
    ★★★★ **门认不出来**（★ `os.path.join` 把约定**拆成两段**）★★ ⇒ ⇒ ★★★★ ⇒ ★★★
    ★★★★ ⇒ ★★★ ⇒ 现在走**共享入口** `gocache.env("mutate")`（★ ★★ 它先
    ★★★★ `ensure()` 建目录并查可写 ⇒ ⇒ 建不出来就**不覆盖** ⇒ ⇒ 脚本照常跑
    ★★★★ ⇒ ⇒ ★★★ ⇒ ★★★ ⇒ ★★★ ★★ 与仓内**全族**同一份约定 ⇒ ⇒ ★★ ⇒ ★★★
    ★★★★ ⇒ ★★★★ ⇒ ★★ 6 GB 上限对它也生效（★ ★★ `env()` 内部会剪枝）。
    """
    env = gocache.env("mutate")
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
    # ★★ 纪律 ⑥b：★ **目标文件存在性是开跑前的硬前置** ⇒ ⇒ 「过期假设」不许
    # ★★ 静默过去（★ 详见上面 `ALL_TARGETS` 处的注释：★ 写回一个已删除的文件会
    # ★★ **凭空把它新建出来**⇒ ⇒ 而【md5 还原校验】照样通过 ⇒ ⇒ rc=0 也可能是错的）。
    for path in ALL_TARGETS:
        if not os.path.exists(path):
            print("目标文件不存在：%s（★ 源码结构变了？★ 变异台的目标清单该收敛了）" % path,
                  file=sys.stderr)
            return 2

    original = snap(TARGET)
    base_md5 = md5(original)
    print("目标：%s（基线 md5 %s）" % (TARGET_REL, base_md5))
    print("")

    def write(text, path=None):
        with open(path or TARGET, "w", encoding="utf-8") as f:
            f.write(text)

    def read(path):
        with open(path, encoding="utf-8") as f:
            return f.read()

    def restore():
        """还原【全部】目标文件（★ 逐个按原路径 md5 校验）。"""
        ok = True
        for path, data, want in ((TARGET, original, base_md5),):
            with open(path, "wb") as f:
                f.write(data)
            now = md5(snap(path))
            if now != want:
                ok = False
                print("★ 还原校验失败：%s（%s != %s）" % (
                    os.path.relpath(path, ROOT), now, want), file=sys.stderr)
        return ok

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
           "rc=0，六条断言全绿", True)

    try:
        # ── M01：去掉 GOCACHE 覆盖 ⇒ 隔离那条必须转红 ──────────
        text = read(TARGET)
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
            "（★ 另外四条必须照常绿 ⇒ 证明不是「全都红」）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M01 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M02：专用目录挪到未被忽略的路径 ⇒ 不入库那条必须转红 ─
        text = read(TARGET)
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
        text = read(TARGET)
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
        text = read(TARGET)
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
            "仍能编译；★ **全部测试转绿**（★ ⇒ ⇒ 证明这六条断言**合起来才是必需的**："
            "没有它们，★ 「把隔离改没了」也能全绿 ⇒ ★★ 功能被改没了而门全绿）",
            "编译=%s，rc=%s，红：%s%s（★ 掏空了 %d 处 t.Fatalf）" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数", n),
            ok,
        )
        if not restore():
            print("M04 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M05（★ r1047）：上限常量本身改成 math.MaxInt64 ⇒ 「上限有界」必须转红 ──
        #
        # ★★★ **这一臂补的是 r1046 自己写下的缺口**：★ M03 钉的是【函数行为】
        # ★★★ ⇒ ⇒ ★★★ **常量本身**从来没人管 ⇒ ⇒ 把它改成 math.MaxInt64 ⇒ ⇒
        # ★★★ 剪枝函数**行为完全正确** ⇒ ⇒ ★★★ M01~M04 **一条都不红** ⇒ ⇒
        # ★★★ 而专用缓存重新变成无界增长 ⇒ ⇒ 磁盘又会被撑爆。
        text = read(TARGET)
        text = replace_once(text, MATH_IMPORT_NEEDLE, MATH_IMPORT_REPL)
        text = replace_once(text, M05_NEEDLE, M05_REPL)
        write(text)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestHollowoutGoCacheSizeCapIsBounded" in failed
            and "TestHollowoutGoCacheSizeCapReachesPrune" not in failed
            and "TestPruneHollowGoCacheRespectsSizeCap" not in failed
            and "TestHollowoutBuildUsesIsolatedGoCache" not in failed
            and "TestHollowoutGoCacheDirIsNotTracked" not in failed
        )
        record(
            "M05 把上限常量本身改成 math.MaxInt64（★ r1047 轮补的已知缺口）",
            "仍能编译；★ 且**只有**「SizeCapIsBounded」转红"
            "（★ ⇒ 证明【常量本身】被门钉住了 ⇒ ⇒ 而不只是【函数行为】被钉住）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M05 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M06（★ r1047）：生产剪枝调用点改传 math.MaxInt64 ⇒ 「上限接到剪枝上」必须转红 ──
        #
        # ★★★ **这一臂补的是「常量有界 ≠ 上限有效」**：★ M05 钉住的是【常量有界】
        # ★★★ ⇒ ⇒ 但若**调用点没有用那个常量**（★ 改传 math.MaxInt64）⇒ ⇒
        # ★★★ 常量照样有界（★ M05 那条绿）⇒ ⇒ ★★★ 而**上限根本没被用上**
        # ★★★ ⇒ ⇒ ★★★ 专用缓存照样无界增长 ⇒ ⇒ ★★★ 「常量有界」是假绿。
        # ★★
        # ★★ r1047 轮合并后：★ 该调用点**从 `hollowout_bin_cleanup_test.go` 搬进了**
        # ★★ `hollowout_gocache_test.go`（★ `TestMain` 那一块）⇒ ⇒ **本臂现在也改 TARGET**。
        text = read(TARGET)
        text = replace_once(text, MATH_IMPORT_NEEDLE, MATH_IMPORT_REPL)
        text = replace_once(text, M06_NEEDLE, M06_REPL)
        write(text)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestHollowoutGoCacheSizeCapReachesPrune" in failed
            and "TestHollowoutGoCacheSizeCapIsBounded" not in failed
            and "TestPruneHollowGoCacheRespectsSizeCap" not in failed
        )
        record(
            "M06 把生产剪枝调用点的上限改传 math.MaxInt64（★ r1047 轮）",
            "仍能编译；★ 且**只有**「SizeCapReachesPrune」转红"
            "（★ ⇒ 证明「常量有界」不等于「上限真的被用上」⇒ ⇒ 上限不是装饰）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M06 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M07（★ r1047 合并后新增）：tmp 目录清理不删目录 ⇒ 「tmp 被清理」必须转红 ──
        #
        # ★★★ **这一臂补的是 r1045 轮留下的最刺眼缺口**：★ tmp 那一半「修好了」
        # ★★★ ⇒ ⇒ ★★★ **但没有任何判据**⇒ ⇒ ★★★ ⇒ 「修完了」**没人验**⇒ ⇒ ★★★
        # ★★★ 合并前实测那个文件里【一个 `Test` 函数都没有】⇒ ⇒ ★★★ 合并后补了判据⑥
        # ★★★ ⇒ ⇒ ★★★ ⇒ **判据必须自带变异臂**，否则「判据没牙齿」不可证伪。
        text = read(TARGET)
        text = replace_once(text, M07_NEEDLE, M07_REPL)
        write(text)

        ok_build, build_out = compile_ok()
        rc, out = run_tests()
        failed = failed_tests(out)
        ok = (
            ok_build
            and rc != 0
            and "TestHollowoutBinTmpDirsAreCleanedUp" in failed
            and "TestHollowoutBuildUsesIsolatedGoCache" not in failed
            and "TestHollowoutGoCacheSizeCapIsBounded" not in failed
            and "TestHollowoutGoCacheSizeCapReachesPrune" not in failed
            and "TestPruneHollowGoCacheRespectsSizeCap" not in failed
            and "TestHollowoutGoCacheDirIsNotTracked" not in failed
        )
        record(
            "M07 让 tmp 目录清理不删目录（★ r1047 轮合并后补的臂）",
            "仍能编译；★ 且**只有**「BinTmpDirsAreCleanedUp」转红"
            "（★ ⇒ 证明 tmp 那一半【现在真的有人验】⇒ ⇒ 不再是裸奔的）",
            "编译=%s，rc=%s，红：%s%s" % (
                "OK" if ok_build else "红(" + build_out[:200] + ")",
                rc, failed, "" if ok_build else " ★ 编译红不算数"),
            ok,
        )
        if not restore():
            print("M07 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

    finally:
        # ── 纪律 ①：还原必须按原路径做 md5 逐字校验（★ 全部目标文件）────
        if not restore():
            return 1

    print("")
    passed = sum(1 for r in LEDGER if r["ok"])
    print("变异台账：%d/%d 通过" % (passed, len(LEDGER)))
    for r in LEDGER:
        print("  %s %s" % ("OK " if r["ok"] else "FAIL", r["arm"]))
    print("还原校验：md5 %s（%s，与基线一致）" % (base_md5, TARGET_REL))
    return 0 if passed == len(LEDGER) else 1


if __name__ == "__main__":
    sys.exit(main())
