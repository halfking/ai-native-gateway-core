#!/usr/bin/env python3
"""candidate_failure_monitor「S4 停写假阳性二分类」门的变异验证脚本（R68 修复）。

标的（`bg/candidate_failure_monitor.go`，R68 提交 `aee31a117`）：
  ① **分类器** `candidateFailureMonitorClassifyStale(failureSourceMaxID)`（纯函数）
     —— >0 ⇒ failure writer 曾活跃 ⇒ 视作 S4 退役/停写 ⇒ **WARN**
     —— =0 ⇒ 从未写入过 ⇒ writer 真坏 ⇒ **ERROR**
  ② **checkStaleness** 的 switch 分派 + 两个诊断字段透传
     （`source_max_failure_id` / `source_max_request_id`）+ SQL 侧读
     `max(aggregation_id)`（failure 侧）与 `max(id)`（request 侧）
  ③ 一条**不该被连带改坏**的既有行为：`failureLogIsStale(lastRequest=nil) ⇒ 不告警`

每条变异改坏一处 ⇒ **指定的**判据必须变红；另有一条**对照变异**（只改注释）⇒ 必须仍然绿。

★ 2026-10-21（批判式审计轮）新增两条：
  ① **M10-关系腿**（红）：把 `switch` 的实参换成另一个变量 ⇒ 日志透传的标识符与
     被分类的值**不再是同一个** ⇒ 判据必须红（这钉的是「关系不变式」，
     而不是「某个名字出现过」）。
  ② **R1-反向变异**（必须仍绿）：把局部变量 `fid` **改名**成 `failureID`
     ——**语义完全不变**的重构 ⇒ 判据必须仍然全绿。
     ⇒ 这条是「判据钉的是关系、不是名字」的**唯一直接证据**：
       旧版 needle（`"source_max_failure_id"\\s*,\\s*fid`）在 R1 下**必然变红**
       ⇒ 那就是「钉太紧 ⇒ 假红」的实证。

★ 为什么本脚本用「**隔离副本**」而不是就地改共享工作树：
  本仓是**共享仓 + 并行会话**（实测 10 个会话并发）。就地改源文件的那 1~2 分钟窗口里，
  若并行会话恰好 `git commit -a`，**被故意改坏的源码就会被推上 main**
  （同款前科：openpocket §941 —— 变异批次被中止后，被改坏的生产代码留在工作树上，
  那不是「一个脏文件」，那是**一个活着的缺陷**）。
  ⇒ 本脚本把 HEAD 用 `git archive` 导出到临时目录（`vendor/` 被跟踪 ⇒ 副本可独立编译），
  **全部变异只在副本里发生**，共享工作树**一个字节都不动**；脚本收尾会核
  `git status --short` 仍为空。
  ⇒ 存量三份脚本（`mutate-s4-recorded-cost-skip-gate.py` /
  `mutate-tuning-view-coordination-gate.py` / `mutate-autoroute-index-sql-gate.py`）
  仍是**就地形态** ⇒ 已登记为遗留项（handoff §12.5-14）。

用法：python3 scripts/mutate-candidate-failure-monitor-classify-gate.py
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
TARGET = "bg/candidate_failure_monitor.go"

# ★ 过滤必须覆盖**全部**相关判据：漏一条 ⇒ 那条腿不会被跑 ⇒
#   `go test` 输出 `no tests to run` 且退出码 0 ⇒ 脚本会把它读成「全绿」⇒ **假绿**。
RUN_FILTER = "ClassifyStale|LogsWarnOnS4Stop|KeepsQuietGatewayBaseline|FailureLogIsStale|StripGoSourceComments"

LEG_CLASSIFIER = "TestCandidateFailureMonitorClassifyStale"
LEG_SHAPE = "TestCheckStalenessLogsWarnOnS4StopScenario"
LEG_QUIET = "TestFailureLogIsStaleKeepsQuietGatewayBaseline"

# 变异定义：名字 / 期望 / 期望红的那条腿 / 编辑列表
#   编辑 = (旧文本, 新文本, 期望出现次数)
#   ★ 出现次数必须**恰好**等于期望值（不是「>=1」）—— 锚点不唯一 ⇒ 改的地方不确定
#     ⇒ 变异不可复现。（2026-10-21 实测：本文件里 `"source_max_failure_id", fid,`
#     出现 **2 次**（WARN 分支 + ERROR 分支各一次）⇒ 那条变异必须两处同改。）
MUTATIONS = [
    (
        "M1-分类器判定条件接反(分类器腿)",
        "red", LEG_CLASSIFIER,
        [("if failureSourceMaxID > 0 {\n\t\treturn stalenessLikelyStopWrite\n\t}",
          "if failureSourceMaxID < 0 {\n\t\treturn stalenessLikelyStopWrite\n\t}", 1)],
    ),
    (
        "M2-分类器返回常量互换(分类器腿·另一条实现路径)",
        "red", LEG_CLASSIFIER,
        [("if failureSourceMaxID > 0 {\n\t\treturn stalenessLikelyStopWrite\n\t}",
          "if failureSourceMaxID > 0 {\n\t\treturn stalenessWriterNeverWrote\n\t}", 1)],
    ),
    (
        "M3-停写分支改走 ERROR(WARN 腿)",
        "red", LEG_SHAPE,
        [('slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)",',
          'slog.Error("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)",', 1)],
    ),
    (
        "M4-真坏分支改走 WARN(ERROR 腿)",
        "red", LEG_SHAPE,
        [('slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)",',
          'slog.Warn("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)",', 1)],
    ),
    (
        "M5-删掉两个诊断字段的透传(字段腿) ★本脚本的发现点",
        "red", LEG_SHAPE,
        # ⚠ 第一版把这两行**整行删掉** ⇒ `fid` / `rid` 变成未使用局部变量 ⇒
        #   **build failed**（编译红 ≠ 断言红 ⇒ 变异不合格；且「门无牙」**并未**被证明）。
        #   ⇒ 改成**外科式**：保留变量使用，只把字面量换成不含目标词的形状
        #   （`"src_max_failure_id"` 不含 `source_max_failure_id`；
        #   `sourceMaxRequestIDKey` 换成字面量 `"src_max_request_id"`）。
        [('\t\t\t"source_max_failure_id", fid,\n',
          '\t\t\t"src_max_failure_id", fid,\n', 2),
         ("\t\t\tsourceMaxRequestIDKey, rid,\n",
          '\t\t\t"src_max_request_id", rid,\n', 2)],
    ),
    (
        "M6-SQL 侧把 max(aggregation_id) 换回 max(ts)(SQL 腿)",
        "red", LEG_SHAPE,
        [("(SELECT max(aggregation_id) FROM candidate_failure_logs_with_current_month),",
          "(SELECT max(ts) FROM candidate_failure_logs_with_current_month),", 1)],
    ),
    (
        "M7-switch 绕过纯函数(分类器调用腿)",
        "red", LEG_SHAPE,
        [("switch candidateFailureMonitorClassifyStale(fid) {",
          "switch candidateFailureMonitorClassifyStale(0) {", 1)],
    ),
    (
        "M8-nil-request 不再静默(既有行为腿)",
        "red", LEG_QUIET,
        [("if lastRequest == nil {\n\t\treturn false\n\t}",
          "if lastRequest == nil {\n\t\treturn true\n\t}", 1)],
    ),
    (
        "M10-switch 实参换成另一个变量(关系腿·2026-10-21 新增)",
        "red", LEG_SHAPE,
        # ★ 判据第二处修复后新增的**关系不变式**：日志里透传的标识符必须与
        #   分类器 switch 的实参是**同一个**。这里把 switch 的实参换成 `rid`
        #   （request 侧的 max id）⇒ 日志仍透传 `fid` ⇒ 两者**不一致**
        #   ⇒ 透传的不是「被分类的那个值」⇒ 门必须红。
        #   ⚠ 语义上这也是真实缺陷：拿 request 侧的 max id 去分类，
        #   会把「failure writer 从未写过」误判成「曾活跃过」（或反之）。
        [("\tswitch candidateFailureMonitorClassifyStale(fid) {\n",
          "\tswitch candidateFailureMonitorClassifyStale(rid) {\n", 1)],
    ),
    (
        "R1-语义不变的变量改名(fid→failureID) ★反向变异·必须仍绿",
        "green", "",
        # ★★ 这条是**反向变异**：判据必须钉「关系」而不是「名字」。
        #   `fid` → `failureID` 是**语义完全不变**的重构（局部变量改名，
        #   分类器实参与日志透传值都还是同一个值）⇒ 判据**必须仍然全绿**。
        #   ⇒ 若它变红 ⇒ 判据把「实现的命名」当成了「行为不变式」
        #     ⇒ 判据会反向保护命名 ⇒ 假红会让人习惯性忽略这道门。
        #   （反向验证：旧版 needle `"source_max_failure_id"\s*,\s*fid`
        #     在这条变异下**必然变红** ⇒ 那就是「钉太紧」的实证。）
        [("\tfid := int64(0)\n", "\tfailureID := int64(0)\n", 1),
         ("\t\tfid = *failureSourceMaxID\n", "\t\tfailureID = *failureSourceMaxID\n", 1),
         ("\tswitch candidateFailureMonitorClassifyStale(fid) {\n",
          "\tswitch candidateFailureMonitorClassifyStale(failureID) {\n", 1),
         ('\t\t\t"source_max_failure_id", fid,\n',
          '\t\t\t"source_max_failure_id", failureID,\n', 2)],
    ),
    (
        "C0-只改注释(对照, 必须仍绿)",
        "green", "",
        [("// 日志与告警体，让告警接收端能根据业务上下文（S4 退役窗口表）决定是否抑制。",
          "// 日志与告警体，让告警接收端能根据业务上下文（S4 退役窗口表）决定是否抑制。（对照：仅改注释）", 1)],
    ),
]

# ★ 每一轮的输出都**落盘**：出现「未解析」时**直接看那个文件**（别再靠猜）。
#   （2026-10-20 实测教训：输出丢掉 ⇒ 两次都查不出原因、只能误记成「环境级瞬时」。）
_RUN_DIR = pathlib.Path(tempfile.gettempdir()) / "mutate-cfm-classify-gate"
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


def assertion_fails(out):
    """**具名断言红** —— 真正触发了目标腿。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")]


def compile_fails(out):
    """**编译级失败** —— 包根本编译不过。

    ⚠ 它只证明「代码被改了」，**不证明目标腿会触发** ⇒ 这类变异不合格。
    """
    return [l.strip() for l in out.splitlines()
            if "build failed" in l or "cannot use" in l or "undefined:" in l
            or "declared and not used" in l or "imported and not used" in l]


def no_tests_ran(out):
    """★ harness 骗人的形态之一：**测试根本没跑** ⇒ 判**作废**（不是绿）。"""
    return ("no tests to run" in out) or ("no test files" in out)


def make_isolated_copy(dst):
    """把 HEAD 导出成一个**可独立编译**的副本（`vendor/` 被 git 跟踪 ⇒ 一并导出）。"""
    r = subprocess.run(
        "git archive HEAD | tar -x -C " + str(dst),
        shell=True, cwd=REPO, capture_output=True, encoding='utf-8', errors='replace',
    )
    if r.returncode != 0:
        raise RuntimeError("git archive 失败: " + (r.stderr or r.stdout))


def main():
    # ★ 前置条件：**已跟踪**文件必须与 HEAD 一致 —— 本脚本导出的是 **HEAD**，
    #   未提交的改动**不会**进副本 ⇒ 若已跟踪文件是脏的，副本就不是你以为的那棵树。
    #   ⚠ **未跟踪文件（`??`）不算**：新建的脚本/文档不进副本，也不影响被测代码。
    st = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                        capture_output=True, encoding='utf-8', errors='replace')
    tracked_dirty = [l for l in st.stdout.splitlines()
                     if l.strip() and not l.startswith("??")]
    if tracked_dirty:
        print("!! 已跟踪文件有未提交改动 ⇒ 本脚本导出的是 HEAD，副本与你的工作树不同源 ⇒ 先提交再跑：")
        print("\n".join(tracked_dirty))
        return 2

    iso = pathlib.Path(tempfile.mkdtemp(prefix="mutate-cfm-iso-"))
    make_isolated_copy(iso)
    target = iso / TARGET
    if not target.exists():
        print(f"!! 隔离副本里没有 {TARGET}")
        return 2
    orig = target.read_text(encoding='utf-8')

    # ★ 锚点唯一性**先验**（不等变异跑到一半才发现）：出现次数必须**恰好**等于期望。
    bad_anchor = []
    for name, expect, leg, edits in MUTATIONS:
        for old, _new, want_n in edits:
            n = orig.count(old)
            if n != want_n:
                bad_anchor.append(f"{name}: 锚点出现 {n} 次（期望 {want_n}）")
                print(f"!! {name}: 锚点文本在 {TARGET} 里出现 {n} 次（期望 {want_n}）⇒ 跳过")
    if bad_anchor:
        print("\n=== 锚点先验不通过，变异验证作废")
        return 2

    failures = []
    try:
        for name, expect, expect_leg, edits in MUTATIONS:
            # 每条变异之前把目标文件还原回原始态（上一条的残留会污染下一条）。
            target.write_text(orig, encoding='utf-8')
            for old, new, _want_n in edits:
                target.write_text(target.read_text(encoding='utf-8').replace(old, new))

            green, out = run_gate(iso)

            if no_tests_ran(out):
                print(f"!!   {name}: 输出含「no tests to run」⇒ **测试根本没跑** ⇒ 本条作废（不是绿）")
                failures.append(name)
                continue

            if expect == "red":
                if green:
                    print(f"FAIL {name}: 变异后判据仍然全绿 ⇒ 这条腿抓不住该缺陷"
                          f"（⇒ **判据没有牙**，或该变异无效）")
                    failures.append(name)
                    continue
                a = assertion_fails(out)
                c = compile_fails(out)
                if a:
                    # ★ 钉住「期望红的那条腿」：红了 ≠ 验到了你想验的那条。
                    hit = [l for l in a if expect_leg and expect_leg in l]
                    if expect_leg and not hit:
                        print(f"!!   {name}: 判据变红了，但**没红在期望的腿**"
                              f"（期望 {expect_leg}；实红 {'; '.join(a)}）⇒ 变异验到的不是这条")
                        failures.append(name)
                    else:
                        print(f"OK   {name}: 判据变红（**断言红**）: {'; '.join(a)}")
                elif c:
                    print(f"!!   {name}: 只有**编译级失败**（{'; '.join(c)}）"
                          f" ⇒ 没证明目标腿会触发 ⇒ 变异不够外科")
                    failures.append(name)
                else:
                    print(f"OK   {name}: 判据变红（未解析出腿名）")
                    print(f"    ⇒ 原始输出已落盘：{_RUN_DIR}/run-{_run_seq[0]:02d}.log（**去看它**）")
            else:
                # ★ 对照分支**必须先判 `green`**（否则绿会被报成红、再重跑、再被报成「不稳定」）。
                if green:
                    print(f"OK   {name}: 判据仍然全绿（无假阳性）")
                    continue
                fails = [l.strip() for l in out.splitlines()
                         if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")]
                c = compile_fails(out)
                kind = "断言红" if fails else ("编译级失败" if c else "未解析")
                print(f"??  {name}: 第一轮变红（{kind}），失败腿 = {fails or '(未解析出腿名)'}；重跑一次判定真假阳性")
                print(f"    ⇒ 原始输出已落盘：{_RUN_DIR}/run-{_run_seq[0]:02d}.log")
                green2, out2 = run_gate(iso)
                if no_tests_ran(out2):
                    print(f"!!   {name}: 重跑输出含「no tests to run」⇒ 作废")
                    failures.append(name)
                elif green2:
                    print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                else:
                    print(f"FAIL {name}: 重跑仍然变红 ⇒ 门有假阳性")
                    failures.append(name)
    finally:
        target.write_text(orig, encoding='utf-8')
        shutil.rmtree(iso, ignore_errors=True)

    # 还原校验（副本侧）+ **共享工作树未被改动**校验（真实工作树侧）。
    if target.exists() and target.read_text(encoding='utf-8') != orig:
        print(f"!! 隔离副本里的 {TARGET} 未还原干净")
        failures.append("restore")
    st2 = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                         capture_output=True, encoding='utf-8', errors='replace')
    tracked_dirty2 = [l for l in st2.stdout.splitlines()
                      if l.strip() and not l.startswith("??")]
    if tracked_dirty2:
        print("!! 共享工作树的已跟踪文件被改动了（本脚本的设计前提就是「一个字节都不动」）:")
        print("\n".join(tracked_dirty2))
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
