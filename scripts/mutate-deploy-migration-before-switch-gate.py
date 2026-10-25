#!/usr/bin/env python3
"""部署链「切流前必须先应用 pending 迁移」门的变异验证脚本（R77 轮建）。

每条变异改坏一处 ⇒ **指定的**判据必须变红；另有一条**对照变异**（只改注释）
⇒ 判据必须**仍然绿**。

★ **隔离副本形态**（照 scripts/mutate-candidate-failure-monitor-classify-gate.py 的范本）：
  变异只发生在 `git archive HEAD` 导出的**副本**里 ⇒ 共享工作树的源码一个字节都不动。
  ⇒ ① 并行会话在变异窗口里 `git commit -a` 不会把改坏的源码推上 main；
    ② 本脚本被 cancel / 崩掉 ⇒ finally 不执行 ⇒ **也不会**在共享树里留下残留。

★ 脚手架纪律（与仓内既有脚本一致）：
  · 跑门前置条件：**已跟踪**文件必须与 HEAD 一致（否则副本不是同一棵树 ⇒ exit 2）；
  · 「no tests to run」判**作废**（不是绿）；
  · 每条变异**钉住期望红的那条腿**（红了 ≠ 验到了你想验的那条）；
  · **编译级失败不算数**（只证明代码被改了，不证明目标腿会触发）。

用法：python3 scripts/mutate-deploy-migration-before-switch-gate.py
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
TEST = "./scripts/ursmcheck/"
RUN_FILTER = (
    "TestDeployMigrationRunsBeforeTrafficSwitch|"
    "TestDeployMigrationFailureAbortsBeforeSwitch|"
    "TestInstallerLegCarriesMigration891|"
    "TestDeployRevisionChannelCarriesMigration891|"
    "TestBareRestartLegIsPinnedAsKnownGap|"
    "TestDeployMigrationOrderGateHasTeeth"
)

# ★ 本门是**新增**文件 ⇒ 它不在 HEAD 里 ⇒ `git archive HEAD` 的副本**没有**它
#   ⇒ 必须显式把它拷进副本（否则副本里跑的是「没有这道门」的世界 ⇒ 全部变异都会
#   因「找不到包/文件」而空跑 ⇒ 假红假绿都看不出来）。
GATE_REL = "scripts/ursmcheck/deploy_migration_before_switch_gate_test.go"

# (名字, 相对路径, 旧文本, 新文本, 期望, 期望红的那条腿)
MUTATIONS = [
    ("M1-把迁移调用挪到切流之后(顺序腿)",
     "scripts/deploy-seamless.sh",
     '  log "[6.5/9] 切换前 pending 迁移 + db-changelog"\n'
     '  deploy_apply_pending_migrations "$SSH_CMD" "$(_env_file_for_target)" "$TARGET" "$seq_val" "$(git rev-parse --short HEAD)" \\\n'
     '    || { err "DB 迁移失败，中止（未切换符号链接）"; exit 1; }\n',
     '  log "[6.5/9] 切换前 pending 迁移 + db-changelog"\n',
     "red", "TestDeployMigrationRunsBeforeTrafficSwitch"),

    ("M2-吞掉迁移失败(失败阻断腿)",
     "scripts/deploy-seamless.sh",
     '|| { err "DB 迁移失败，中止（未切换符号链接）"; exit 1; }',
     '|| { err "DB 迁移失败（但继续）"; true; }',
     "red", "TestDeployMigrationFailureAbortsBeforeSwitch"),

    ("M3-安装器 StartupFiles 去掉 891(安装器腿)",
     "installer/internal/dbinit/runner.go",
     '\t\t\t"891_session_turns_cost_negative_partial_idx.sql",\n',
     '',
     "red", "TestInstallerLegCarriesMigration891"),

    ("M4-部署链通道去掉 891(升级腿)",
     "scripts/apply-db-revision-sequence.sh",
     '  "$ROOT_DIR/sql/migrations/startup/891_session_turns_cost_negative_partial_idx.sql"\n',
     '',
     "red", "TestDeployRevisionChannelCarriesMigration891"),

    # ★ M5/M5b 的注入点选在**包级 const 之后**（不是 `package db` 之后）：
    #   上一版把 var 注入在 `package db` 与 `import (` 之间 ⇒ Go 报
    #   「imports must appear before other declarations」⇒ **编译级失败**
    #   ⇒ 变异不合格（只证明代码被改了，不证明目标腿会触发）。
    ("M5-ensure 链吸收 891 的索引(裸重启腿·现状变了)",
     "db/db.go",
     "const candidateFailureLogsHeapPartitionsBudget = 5 * time.Minute",
     "const candidateFailureLogsHeapPartitionsBudget = 5 * time.Minute\n\n"
     "// ★ 变异注入：假装 ensure 链开始吸收编号迁移的索引。\n"
     "var mutatedEnsureIdxName = \"idx_session_turns_cost_negative\"\n",
     "red", "TestBareRestartLegIsPinnedAsKnownGap"),

    ("M5b-索引名只出现在注释里(反向变异·必须仍绿)",
     "db/db.go",
     "const candidateFailureLogsHeapPartitionsBudget = 5 * time.Minute",
     "const candidateFailureLogsHeapPartitionsBudget = 5 * time.Minute\n\n"
     "// ★ 反向变异：只在**注释**里提到索引名（文档提及）⇒ J5 必须**仍绿**\n"
     "//   （J5 判的是「ensure 链**不创建**它」，不是「名字出现过」）⇒ 见 idx_session_turns_cost_negative\n",
     "green", ""),

    ("C0-只改注释(对照, 必须仍绿)",
     "scripts/deploy-seamless.sh",
     '  log "[6.5/9] 切换前 pending 迁移 + db-changelog"',
     '  log "[6.5/9] 切换前 pending 迁移 + db-changelog"  # 对照：仅改注释',
     "green", ""),
]


def run_gate(cwd):
    r = subprocess.run(
        ["go", "test", TEST, "-run", RUN_FILTER, "-count=1"],
        cwd=cwd, capture_output=True, encoding='utf-8', errors='replace',
    )
    out = r.stdout + r.stderr
    return r.returncode == 0, out


def assertion_fails(out):
    """**具名断言红** —— 真正触发了目标腿。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")]


def compile_fails(out):
    """**编译级失败** —— 包根本编译不过（只证明代码被改了，不证明目标腿会触发）。"""
    return [l.strip() for l in out.splitlines()
            if "build failed" in l or "cannot use" in l or "undefined:" in l
            or "declared and not used" in l or "imported and not used" in l]


def no_tests_ran(out):
    """★ 「测试根本没跑」⇒ 判**作废**（不是绿）。"""
    return ("no tests to run" in out) or ("no test files" in out)


def tracked_dirty_lines():
    """**已跟踪**文件的未提交改动（未跟踪的 `??` 不算）。"""
    st = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                        capture_output=True, encoding='utf-8', errors='replace')
    return [l for l in st.stdout.splitlines()
            if l.strip() and not l.startswith("??")]


def make_isolated_copy(dst):
    """把 HEAD 导出成一个**可独立编译**的副本（`vendor/` 被 git 跟踪 ⇒ 一并导出）。"""
    r = subprocess.run(
        "git archive HEAD | tar -x -C " + str(dst),
        shell=True, cwd=REPO, capture_output=True, encoding='utf-8', errors='replace',
    )
    if r.returncode != 0:
        raise RuntimeError("git archive 失败: " + (r.stderr or r.stdout))


def main():
    # ★ 前置条件：**已跟踪**文件必须与 HEAD 一致 —— 本脚本导出的是 **HEAD**。
    dirty = tracked_dirty_lines()
    if dirty:
        print("!! 已跟踪文件有未提交改动 ⇒ 本脚本导出的是 HEAD，副本与你的工作树不同源 ⇒ 先提交再跑：")
        print("\n".join(dirty))
        return 2

    iso = pathlib.Path(tempfile.mkdtemp(prefix="mutate-deploy-mig-iso-"))
    make_isolated_copy(iso)

    # ★ 把这道**新增**的门拷进副本（它不在 HEAD 里）。
    gate_dst = iso / GATE_REL
    gate_dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(REPO / GATE_REL, gate_dst)
    if not gate_dst.exists():
        print(f"!! 隔离副本里没有 {GATE_REL}")
        shutil.rmtree(iso, ignore_errors=True)
        return 2

    originals = {}
    for name, rel, old, new, expect, leg in MUTATIONS:
        p = iso / rel
        if rel not in originals:
            if not p.exists():
                print(f"!! 隔离副本里没有 {rel}")
                shutil.rmtree(iso, ignore_errors=True)
                return 2
            originals[rel] = (p, p.read_text(encoding='utf-8'))
        # ★ 锚点唯一性**先验**：出现次数必须**恰好**等于 1，否则变异会落在别处（空跑）。
        n = originals[rel][1].count(old)
        if n != 1:
            print(f"!! {name}: 锚点在 {rel} 里出现 {n} 次（期望 1）⇒ 变异会落在别处 ⇒ 作废")
            shutil.rmtree(iso, ignore_errors=True)
            return 2

    failures = []
    try:
        for name, rel, old, new, expect, expect_leg in MUTATIONS:
            path, orig = originals[rel]
            # 每条变异之前把**所有**文件还原回原始态。
            for p, o in originals.values():
                p.write_text(o, encoding='utf-8')

            path.write_text(orig.replace(old, new, 1), encoding='utf-8')
            green, out = run_gate(iso)

            if no_tests_ran(out):
                print(f"!!   {name}: 输出含「no tests to run」⇒ **测试根本没跑** ⇒ 本条作废（不是绿）")
                failures.append(name)
                continue

            if expect == "red":
                if green:
                    print(f"FAIL {name}: 变异后判据仍然全绿 ⇒ 这条腿抓不住该缺陷")
                    failures.append(name)
                    continue
                a = assertion_fails(out)
                c = compile_fails(out)
                if a:
                    hit = [l for l in a if expect_leg and expect_leg in l]
                    if expect_leg and not hit:
                        print(f"!!   {name}: 判据变红了，但**没红在期望的腿**"
                              f"（期望 {expect_leg}；实红 {'; '.join(a)}）⇒ 变异验到的不是这条")
                        failures.append(name)
                    else:
                        print(f"OK   {name}: 判据变红（**断言红**，且红在期望的腿 {expect_leg}）: {'; '.join(a)}")
                elif c:
                    print(f"!!   {name}: 只有**编译级失败**（{'; '.join(c)}）"
                          f" ⇒ 没证明目标腿会触发 ⇒ 变异不够外科")
                    failures.append(name)
                else:
                    print(f"OK   {name}: 判据变红（未解析出腿名）")
            else:
                if green:
                    print(f"OK   {name}: 判据仍然全绿（无假阳性）")
                    continue
                a = assertion_fails(out)
                c = compile_fails(out)
                kind = "断言红" if a else ("编译级失败" if c else "未解析")
                print(f"??  {name}: 第一轮变红（{kind}）；重跑一次判定是真假阳性还是不稳定")
                green2, out2 = run_gate(iso)
                if no_tests_ran(out2):
                    print(f"!!   {name}: 重跑输出含「no tests to run」⇒ 作废")
                    failures.append(name)
                elif green2:
                    print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                else:
                    print(f"FAIL {name}: 重跑仍然变红 ⇒ 门有假阳性，失败腿 = "
                          f"{'; '.join(assertion_fails(out2))}")
                    failures.append(name)
    finally:
        for path, orig in originals.values():
            path.write_text(orig, encoding='utf-8')
        shutil.rmtree(iso, ignore_errors=True)

    # ★ 共享工作树**未被改动**校验（真实工作树侧）—— 隔离副本形态的核心承诺。
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
