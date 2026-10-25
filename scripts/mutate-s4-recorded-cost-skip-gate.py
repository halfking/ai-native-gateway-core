#!/usr/bin/env python3
"""S4 跳过门（SkipWhen 机制 + 退役不变式）的变异验证脚本（R51 续轮建；D0 轮重写）。

每条变异改坏一处 ⇒ **指定的**判据必须变红。
另有一条**对照变异**（只改日志文案）⇒ 判据必须**仍然绿**。

★ 2026-10-20 重写的原因（上游 D0 轮把本脚本的标的退役了）：
并行审计轮 D0 + 迁移 885 把「recorded_cost_is_negative 挂 S4 停写门」这道**具体守卫**
退役了（理由：885 把 v1 族整体 DROP ⇒「源不再前进」的前提消失；且
`storage.request_logs_write_enabled` 的**代码默认**被翻成 false ⇒ 门恒闭 ⇒
「不可判定」被上报成「没问题」）。
⇒ 本脚本的标的从「那道守卫」换成**退役后仍在的两件事**：
  ① **不变式**：`recorded_cost_is_negative` **不得**再挂 SkipWhen
     （`TestRecordedCostCheckCarriesNoS4Guard` / `TestOnlyTheFrozenSourceCheckIsGuarded`）；
  ② **机制**：`SkipWhen` 这套机制本身仍活着、仍必须「先于 SQL 短路」且「跳过可观测」
     （改用合成检查驱动）。
⇒ ⚠ 重写时同时修掉一个**会骗人的 harness 缺陷**：旧的 `-run` 过滤
  （`RecordedCost|HealthCheckSkip|OnlyTheFrozenSource|…`）**漏掉了**
  `TestSkipWhenMechanismStillShortCircuitsBeforeQuery` ⇒ 那条腿**根本没被跑**
  ⇒ `go test` 输出 `no tests to run` ⇒ 脚本会把它读成「全绿」⇒ **假绿**。
  ⇒ 现在：① 过滤覆盖全部 6 条；② **「no tests to run」判作废**（不是绿）；
  ③ 每条变异**钉住期望红的那条腿**（不只看「有没有红」）。

用法：python3 scripts/mutate-s4-recorded-cost-skip-gate.py
"""
# encoding 必须显式指定：read_text()/write_text() 的缺省编码取自 locale，
# 在非 UTF-8 locale（Windows 中文默认 cp936 / GBK）下：读会 UnicodeDecodeError
# （门在判据之前就崩），写会把 UTF-8 源码静默改写成 GBK（门把源码改成乱码）。

import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]
TEST = "./bg/"

# ★ 过滤必须覆盖**全部** 6 条 S4 相关判据。
#   漏一条 ⇒ 那条腿不会被跑 ⇒ 「no tests to run」⇒ 假绿（2026-10-20 实测过这个坑）。
RUN_FILTER = (
    "RecordedCost|OnlyTheFrozenSource|SkipWhenMechanism|SkippedCheckIsRecorded|"
    "RoutingHealthCheckerIsRegistered|S4ScanSkip"
)

# (相对路径, 旧文本, 新文本, 期望, 名字, 期望红的那条腿)
MUTATIONS = [
    ("bg/routing_health_checks.go",
     '\t\t{CheckID: "recorded_cost_is_negative", Severity: "warning", Optional: true,',
     '\t\t{CheckID: "recorded_cost_is_negative", Severity: "warning", Optional: true,\n'
     '\t\t\tSkipWhen: func() (bool, string) { return false, "" },',
     "red", "M1-把守卫加回 recorded_cost(退役不变式腿)",
     "TestRecordedCostCheckCarriesNoS4Guard"),

    ("bg/routing_health_checks.go",
     "\t\t\tif skip, reason := chk.SkipWhen(); skip {",
     "\t\t\tif skip, reason := chk.SkipWhen(); skip && false {",
     "red", "M2-守卫不短路(机制·短路腿)",
     "TestSkipWhenMechanismStillShortCircuitsBeforeQuery"),

    ("bg/routing_health_checks.go",
     "\t\t\tif skip, reason := chk.SkipWhen(); skip {",
     "\t\t\tif skip, reason := chk.SkipWhen(); !skip {",
     "red", "M3-守卫极性接反(机制·极性腿)",
     "TestSkipWhenMechanismStillShortCircuitsBeforeQuery"),

    ("bg/routing_health_checks.go",
     "\t\t\t\tif recorder != nil {",
     "\t\t\t\tif recorder != nil && false {",
     "red", "M4-跳过不记进 recorder(可观测腿)",
     "TestSkippedCheckIsRecordedAndPublished"),

    ("bg/s4_scan_skip_metrics.go",
     "\t\ts4ScanWorkerRoutingHealthChecker,\n",
     "\t\ts4ScanWorkerRoutingHealthChecker + \"__mutated\",\n",
     "red", "M5-worker 不在闭集(闭集·worker 腿)",
     "TestRoutingHealthCheckerIsRegisteredInS4SkipClosedSet"),

    ("bg/s4_scan_skip_metrics.go",
     "\t\tusageArmEmptySkipReason,\n",
     "\t\tusageArmEmptySkipReason + \"__mutated\",\n",
     "red", "M5b-reason 不在闭集(闭集·reason 腿)",
     "TestRoutingHealthCheckerIsRegisteredInS4SkipClosedSet"),

    ("bg/routing_health_checker.go",
     "\tdefer recordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())",
     "\trecordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())",
     "red", "M6-发布不是 defer(接线腿)",
     "TestS4ScanSkipStateIsPublishedOnEveryReturnPath"),

    ("bg/routing_health_checker.go",
     'slog.Info("routing_health_checker: run completed"',
     'slog.Info("routing_health_checker: run completed (v2)"',
     "green", "C0-只改日志文案(对照, 必须仍绿)", ""),
]


# ★ 每一轮的输出都**落盘**（2026-10-20 实测教训）：
#   「对照变异首轮变红、但失败腿一个都解析不出来」这个现象**出现过多次**，
#   而脚本当时**把输出丢掉了** ⇒ 两次都查不出原因、只能记成「环境级瞬时」。
#   ⇒ 现在每一轮都写 /tmp/mutate-s4-run-N.log；下次再遇到「未解析」，
#   **直接看那个文件**（别再靠猜）。
_RUN_DIR = pathlib.Path(tempfile.gettempdir()) / "mutate-s4-recorded-cost-skip-gate"
_run_seq = [0]



# stdout/stderr 必须显式配 UTF-8：这些脚本会打印含「⇒」等字符的中文，
# 而 cp936（Windows 中文默认）编码不了 ⇒ print() 抛 UnicodeEncodeError
# ⇒ 门在出读数时就崩了。在 UTF-8 locale 下本段是 no-op，POSIX 行为不变。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding='utf-8')
    except (AttributeError, ValueError):
        pass

def run_gate(cwd):
    r = subprocess.run(
        ["go", "test", TEST, "-run", RUN_FILTER, "-count=1"],
        cwd=cwd, capture_output=True, encoding='utf-8', errors='replace',
    )
    out = r.stdout + r.stderr
    _run_seq[0] += 1
    try:
        _RUN_DIR.mkdir(parents=True, exist_ok=True)
        (_RUN_DIR / f"run-{_run_seq[0]:02d}.log").write_text(
            f"exit={r.returncode}\n{out}", encoding="utf-8")
    except OSError:
        pass
    return r.returncode == 0, out


def failing_lines(out):
    """把失败的那几条腿点名 —— 没有它，一次「对照变异变红」只能靠猜。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")
            or "build failed" in l or "cannot use" in l]


def assertion_fails(out):
    """**具名断言红** —— 真正触发了目标腿。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")]


def compile_fails(out):
    """**编译级失败** —— 包根本编译不过。

    ⚠ 它只证明「代码被改了」，**不证明目标腿会触发**
    ⇒ 这类变异不合格（详见 scripts/mutate-tuning-view-coordination-gate.py 同名函数的注释：
    2026-10-20 实测 M8/M9 整行删除 ⇒ 局部变量/import 变未使用 ⇒ 只拿到 build failed）。
    """
    return [l.strip() for l in out.splitlines()
            if "build failed" in l or "cannot use" in l or "undefined:" in l
            or "declared and not used" in l or "imported and not used" in l]


def no_tests_ran(out):
    """★ harness 骗人的形态之一：**测试根本没跑**。

    `go test` 在 `-run` 一个都没匹配上时输出 `testing: warning: no tests to run`
    并以 **0 退出** ⇒ 脚本会把它读成「全绿」⇒ **假绿**。
    （2026-10-20 实测：旧的 `-run` 过滤漏掉
    `TestSkipWhenMechanismStillShortCircuitsBeforeQuery` ⇒ 那条腿从来没被验证过。）
    """
    return ("no tests to run" in out) or ("no test files" in out)


def make_isolated_copy(dst):
    """把 HEAD 导出成一个**可独立编译**的副本（`vendor/` 被 git 跟踪 ⇒ 一并导出）。"""
    r = subprocess.run(
        "git archive HEAD | tar -x -C " + str(dst),
        shell=True, cwd=REPO, capture_output=True, encoding='utf-8', errors='replace',
    )
    if r.returncode != 0:
        raise RuntimeError("git archive 失败: " + (r.stderr or r.stdout))


def tracked_dirty_lines():
    """**已跟踪**文件的未提交改动（未跟踪的 `??` 不算）。"""
    st = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                        capture_output=True, encoding='utf-8', errors='replace')
    return [l for l in st.stdout.splitlines()
            if l.strip() and not l.startswith("??")]


def main():
    # ★★ 前置条件：**已跟踪**文件必须与 HEAD 一致 —— 本脚本导出的是 **HEAD**，
    #   未提交的改动**不会**进副本 ⇒ 若已跟踪文件是脏的，副本就不是你以为的那棵树。
    #   ⚠ **未跟踪文件（`??`）不算**：新建的脚本/文档不进副本，也不影响被测代码。
    dirty = tracked_dirty_lines()
    if dirty:
        print("!! 已跟踪文件有未提交改动 ⇒ 本脚本导出的是 HEAD，副本与你的工作树不同源 ⇒ 先提交再跑：")
        print("\n".join(dirty))
        return 2

    # ★★ **隔离副本形态**（2026-10-25 R77 轮落地）：变异**只发生在副本里** ⇒
    #   共享工作树的源码**一个字节都不会被改** ⇒ 两个真实风险同时消失：
    #   ① 并行会话在变异窗口里 `git commit -a` ⇒ 改坏的源码被推上 main；
    #   ② 本脚本被 cancel / 崩掉 ⇒ **finally 不执行** ⇒ 变异残留留在共享工作树里
    #      （2026-10-21 实测过这个坑：cancel 掉带 finally 还原的后台复跑任务 ⇒
    #        `bg/routing_health_checks.go` 的 `skip && false` 残留躺在共享树里
    #        一直到下一次提交 ⇒ 差点被推上 main）。
    iso = pathlib.Path(tempfile.mkdtemp(prefix="mutate-s4-iso-"))
    make_isolated_copy(iso)

    originals = {}
    repo_orig = {}
    for rel, _, _, _, _, _ in MUTATIONS:
        p = iso / rel
        if rel not in originals:
            if not p.exists():
                print(f"!! 隔离副本里没有 {rel}")
                shutil.rmtree(iso, ignore_errors=True)
                return 2
            originals[rel] = (p, p.read_text(encoding='utf-8'))
            # ★ 运行前把**共享工作树**里的目标文件读下来 ⇒ 收尾时逐字比对（见末尾）。
            repo_orig[rel] = (REPO / rel).read_text(encoding='utf-8')

    failures = []
    try:
        for rel, old, new, expect, name, expect_leg in MUTATIONS:
            path, orig = originals[rel]
            if old not in orig:
                print(f"!! {name}: 锚点文本在 {rel} 里找不到，跳过")
                failures.append(name)
                continue
            # ★ 每条变异之前把**所有**文件还原回原始态。
            # 只还原「本条要改的那个文件」是不够的：上一条变异可能改的是**另一个**文件
            # （本脚本的 M5/M5b 改 s4_scan_skip_metrics.go、M6/C0 改 routing_health_checker.go），
            # 残留会污染下一条 ⇒ 表现为「对照变异变红」这种**看似门的假阳性**。
            # （本轮真踩到过：M5 删掉闭集里的 worker 后没还原 ⇒ C0 在缺 worker 的树上跑 ⇒ 红。）
            for p, o in originals.values():
                p.write_text(o, encoding='utf-8')

            path.write_text(orig.replace(old, new, 1), encoding='utf-8')
            green, out = run_gate(iso)

            # ★ 先判「测试到底跑了没」——这是**作废条件**，不是绿/红。
            if no_tests_ran(out):
                print(f"!!   {name}: 输出含「no tests to run」⇒ **测试根本没跑** ⇒ 本条作废"
                      f"（不是绿）")
                failures.append(name)
                continue

            if expect == "red":
                if green:
                    print(f"FAIL {name}: 变异后判据仍然全绿 ⇒ 这条腿抓不住该缺陷")
                    failures.append(name)
                else:
                    a = assertion_fails(out)
                    c = compile_fails(out)
                    if a:
                        # ★ 钉住「期望红的那条腿」：红了 ≠ 验到了你想验的那条。
                        hit = [l for l in a if expect_leg and expect_leg in l]
                        if expect_leg and not hit:
                            print(f"!!   {name}: 判据变红了，但**没红在期望的腿**"
                                  f"（期望 {expect_leg}；实红 {'; '.join(a)}）"
                                  f" ⇒ 变异验到的不是这条")
                            failures.append(name)
                        else:
                            print(f"OK   {name}: 判据变红（**断言红**）: {'; '.join(a)}")
                    elif c:
                        # ⚠ 只有编译级失败 ⇒ 门抓到了改动，但**没证明目标腿会触发**。
                        print(f"!!   {name}: 只有**编译级失败**（{'; '.join(c)}）"
                              f" ⇒ 没证明目标腿会触发 ⇒ 变异不够外科")
                        failures.append(name)
                    else:
                        print(f"OK   {name}: 判据变红（未解析出腿名）")
                        print(f"    ⇒ 原始输出已落盘：{_RUN_DIR}/run-{_run_seq[0]:02d}.log"
                              f"（**去看它** ⇒ 这是「红了但不是断言红」的形态）")
            else:
                # ★★ 对照分支**必须先判 `green`** —— 这是本脚本自己踩到并修掉的一个
                #   **harness bug**（2026-10-20 20:3x 实测）：旧代码在对照分支里
                #   **无条件**打印「第一轮变红」，**从不检查 green** ⇒
                #   对照变异**明明是绿的**，却被报成「变红（未解析）」⇒
                #   然后重跑一次（也是绿的）⇒ 又被报成「上一轮是**不稳定**」。
                #   ⇒ 两轮交接里把这条误读记成了「环境级瞬时」⇒ **结论是错的**。
                #   真因在**量具自己**（分支结构漏判），不在被测对象、也不在环境。
                if green:
                    print(f"OK   {name}: 判据仍然全绿（无假阳性）")
                    continue
                # 真的红了 ⇒ 有两种可能：门有假阳性，或这一轮跑出了**不稳定**。
                # 二者的处理完全不同（前者要改门，后者要改判据的量具），
                # 而「是哪条腿红的」是区分它们的第一手证据。
                # ⇒ 失败时重跑一次并把失败腿打出来；重跑还红 ⇒ 门有假阳性。
                fails = failing_lines(out)
                a = assertion_fails(out)
                c = compile_fails(out)
                kind = "断言红" if a else ("编译级失败" if c else "未解析")
                print(f"??  {name}: 第一轮变红（{kind}），失败腿 = {fails or '(未解析出腿名)'}；重跑一次判定是真假阳性还是不稳定")
                print(f"    ⇒ 原始输出已落盘：{_RUN_DIR}/run-{_run_seq[0]:02d}.log")
                green2, out2 = run_gate(iso)
                if no_tests_ran(out2):
                    print(f"!!   {name}: 重跑输出含「no tests to run」⇒ 作废")
                    failures.append(name)
                elif green2:
                    print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                else:
                    print(f"FAIL {name}: 重跑仍然变红 ⇒ 门有假阳性，失败腿 = {failing_lines(out2)}")
                    failures.append(name)
    finally:
        for path, orig in originals.values():
            path.write_text(orig, encoding='utf-8')
        shutil.rmtree(iso, ignore_errors=True)

    # ★ 共享工作树**未被改动**校验（真实工作树侧）—— 隔离副本形态的核心承诺。
    #   ⚠ 不用「副本侧还原干净」当证据：副本已被 rmtree 删掉 ⇒ 那种检查**恒真**（空检查）。
    for rel, want in repo_orig.items():
        if (REPO / rel).read_text(encoding='utf-8') != want:
            print(f"!! 共享工作树里的 {rel} 与运行前不一致 ⇒ 本脚本不该改动它")
            failures.append("shared-tree-mutated")
    dirty2 = tracked_dirty_lines()
    if dirty2:
        print("!! 共享工作树的已跟踪文件被改动了（本脚本的设计前提就是「一个字节都不动」）:")
        print("\n".join(dirty2))
        failures.append("shared-tree-dirty")
    else:
        print("OK   共享工作树的已跟踪文件未被改动")

    if failures:
        print(f"\n=== 变异验证失败: {failures}")
        return 1
    print("\n=== 变异验证全部通过（每条腿都能红，对照变异不红）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
