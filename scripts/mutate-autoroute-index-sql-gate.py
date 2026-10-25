#!/usr/bin/env python3
"""autoroute SQL 形态门的变异验证脚本（R51 96h 轮）。

每条变异改坏一处 ⇒ 对应判据必须变红。
另有一条**对照变异**（只加注释）⇒ 判据必须**仍然绿**
（证明门不是「任何改动都红」，也不是「注释里的字样也算违规」）。

用法：python3 scripts/mutate-autoroute-index-sql-gate.py
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
TARGET_REL = pathlib.Path("autoroute") / "index.go"
TARGET = REPO / TARGET_REL
TEST = "./autoroute/"

MUTATIONS = [
    # (名称, 旧文本, 新文本, 期望: red=该腿必须红, green=必须仍绿, 期望红的那条腿)
    #
    # ★★ 2026-10-21（批判式审计轮）：补上第 5 列「**期望红的那条腿**」。
    #   原来只判「有断言红了」⇒ **红了 ≠ 验到了你想验的那条**：若上游把某条腿
    #   改名/删除，剩余腿照样能红 ⇒ 脚本照样报「验证通过」⇒ **那条腿从未被验过**。
    #   ⇒ 现在每条变异都**指名**它必须红在哪条腿上；不在这条腿上 ⇒ 判失败。
    #   （腿名是 2026-10-21 **实跑读数**得来的，不是推的。）
    ("M1-DISTINCT-ON(性能腿)",
     "SELECT credential_id, raw_model, MAX(bucket) AS bucket",
     "SELECT DISTINCT ON (credential_id, raw_model) credential_id, raw_model, MAX(bucket) AS bucket",
     "red", "TestAutoRouteRefreshIndexRejectsDISTINCTON"),
    ("M2-去掉分组(语义腿)",
     "    GROUP BY credential_id, raw_model\n)",
     ")",
     "red", "TestAutoRouteRefreshIndexIsPerPairMax"),
    ("M3-换第三种形态(准入闸)",
     "JOIN latest_bucket",
     "JOIN lb_alias",
     "red", "TestAutoRouteRefreshIndexUsesKnownShape"),
    ("M4-把过滤推进去重段(位置腿)",
     "    FROM credential_model_index_with_current_month\n    GROUP BY credential_id, raw_model\n)",
     "    FROM credential_model_index_with_current_month\n    JOIN credentials cr2 ON cr2.id = credential_id\n    GROUP BY credential_id, raw_model\n)",
     "red", "TestAutoRouteRefreshIndexDedupStaysBeforeFilters"),
    ("M5-事实源换成冷父表(事实源腿)",
     "FROM credential_model_index_with_current_month",
     "FROM credential_model_index ",
     "red", "TestAutoRouteRefreshIndexReadsUnionView"),
    ("M6-去掉 ORDER BY(收尾腿)",
     "ORDER BY cmi.canonical_id, cmi.score_smart DESC",
     "",
     "red", "TestAutoRouteRefreshIndexKeepsOrderAndFilters"),
    ("M7-丢掉可用性过滤(收尾腿)",
     "  AND cmb.available = TRUE\n",
     "",
     "red", "TestAutoRouteRefreshIndexKeepsOrderAndFilters"),
    ("C0-只加注释(对照, 必须仍绿)",
     "const refreshIndexSQL = `\n",
     "const refreshIndexSQL = `\n-- 说明：这里刻意没用 DISTINCT ON，原因是 252 真库实测它慢 2~3 倍\n",
     "green", ""),
]



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
        ["go", "test", TEST, "-run", "AutoRouteRefreshIndex", "-count=1"],
        cwd=cwd, capture_output=True, encoding='utf-8', errors='replace',
    )
    return r.returncode == 0, r.stdout + r.stderr


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
    ⇒ 这类变异不合格（详见 scripts/mutate-tuning-view-coordination-gate.py 同名函数的注释）。
    """
    return [l.strip() for l in out.splitlines()
            if "build failed" in l or "cannot use" in l or "undefined:" in l
            or "declared and not used" in l or "imported and not used" in l]


def no_tests_ran(out):
    """★ harness 骗人的形态之一：**测试根本没跑** ⇒ 判**作废**（不是绿）。

    ⚠ 2026-10-21（批判式审计轮）补上：本题脚本原先**没有**这道闸，而
      `scripts/mutate-s4-recorded-cost-skip-gate.py` 记录过它当年正是**被这个坑
      咬过**（旧的 `-run` 过滤漏掉一条腿 ⇒ `go test` 输出「no tests to run」且
      退出码 0 ⇒ 脚本把它读成「全绿」⇒ **假绿**）。
      ⇒ 一旦上游把某个测试函数改名 ⇒ `-run` 失配 ⇒ **全部变异会「全绿」** ⇒
        脚本会报「验证通过」而实际上**一条腿都没跑**。
      ⇒ 这道闸把「没跑」与「跑了且全绿」**区分开**：前者一律判**作废**。
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
    dirty = tracked_dirty_lines()
    if dirty:
        print("!! 已跟踪文件有未提交改动 ⇒ 本脚本导出的是 HEAD，副本与你的工作树不同源 ⇒ 先提交再跑：")
        print("\n".join(dirty))
        return 2

    # ★★ **隔离副本形态**（2026-10-25 R77 轮落地）：变异**只发生在副本里** ⇒
    #   共享工作树的源码**一个字节都不会被改** ⇒ 两个真实风险同时消失：
    #   ① 并行会话在变异窗口里 `git commit -a` ⇒ 改坏的源码被推上 main；
    #   ② 本脚本被 cancel / 崩掉 ⇒ **finally 不执行** ⇒ 变异残留留在共享工作树里。
    iso = pathlib.Path(tempfile.mkdtemp(prefix="mutate-autoroute-iso-"))
    make_isolated_copy(iso)
    target = iso / TARGET_REL
    if not target.exists():
        print(f"!! 隔离副本里没有 {TARGET_REL}")
        shutil.rmtree(iso, ignore_errors=True)
        return 2
    orig = target.read_text(encoding='utf-8')
    # ★ 运行前把**共享工作树**里的目标文件读下来 ⇒ 收尾时逐字比对（见末尾）。
    repo_orig = TARGET.read_text(encoding='utf-8')

    failures = []
    try:
        for name, old, new, expect, expect_leg in MUTATIONS:
            if old not in orig:
                print(f"!! {name}: 锚点文本在源文件里找不到，跳过")
                failures.append(name)
                continue
            target.write_text(orig.replace(old, new), encoding='utf-8')
            green, out = run_gate(iso)
            # ★ 作废闸：测试根本没跑 ⇒ 这条读数**不算绿**（见 no_tests_ran 的注释）。
            if no_tests_ran(out):
                print(f"!!   {name}: 输出含「no tests to run」⇒ **测试根本没跑** ⇒ 本条作废（不是绿）")
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
                                  f"（期望 {expect_leg}；实红 {'; '.join(a)}）⇒ 变异验到的不是这条")
                            failures.append(name)
                        else:
                            print(f"OK   {name}: 判据变红（**断言红**，且红在期望的腿 {expect_leg}）: {'; '.join(a)}")
                    elif c:
                        # ⚠ 只有编译级失败 ⇒ 门抓到了改动，但**没证明目标腿会触发**。
                        print(f"!!   {name}: 只有**编译级失败**（{'; '.join(c)}）"
                              f" ⇒ 没证明目标腿会触发 ⇒ 变异不够外科")
                        failures.append(name)
                    else:
                        print(f"OK   {name}: 判据变红（未解析出腿名）")
            else:
                if green:
                    print(f"OK   {name}: 判据仍然全绿（无假阳性）")
                else:
                    # 对照变异变红有两种可能：门有假阳性，或这一轮跑出了**不稳定**。
                    # ⇒ 重跑一次并把失败腿打出来；重跑还红 ⇒ 门有假阳性。
                    a = assertion_fails(out)
                    c = compile_fails(out)
                    kind = "断言红" if a else ("编译级失败" if c else "未解析")
                    print(f"??  {name}: 第一轮变红（{kind}），失败腿 = "
                          f"{failing_lines(out) or '(未解析出腿名)'}；"
                          f"重跑一次判定是真假阳性还是不稳定")
                    green2, out2 = run_gate(iso)
                    if no_tests_ran(out2):
                        print(f"!!   {name}: 重跑输出含「no tests to run」⇒ 作废")
                        failures.append(name)
                    elif green2:
                        print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                    else:
                        print(f"FAIL {name}: 重跑仍然变红 ⇒ 门把注释当成了违规 / 有假阳性，"
                              f"失败腿 = {failing_lines(out2)}")
                        failures.append(name)
    finally:
        target.write_text(orig, encoding='utf-8')
        shutil.rmtree(iso, ignore_errors=True)

    # ★ 共享工作树**未被改动**校验（真实工作树侧）—— 隔离副本形态的核心承诺。
    #   ⚠ 不用「副本侧还原干净」当证据：副本已被 rmtree 删掉 ⇒ 那种检查**恒真**（空检查）。
    if TARGET.read_text(encoding='utf-8') != repo_orig:
        print(f"!! 共享工作树里的 {TARGET_REL} 与运行前不一致 ⇒ 本脚本不该改动它")
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
