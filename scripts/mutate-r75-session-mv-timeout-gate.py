#!/usr/bin/env python3
"""R75 轮 · session 族 REFRESH 被 180s 语句上限杀掉（分族超时 + 相位错开 + 快视图优先）���变异验证。

用法：python3 scripts/mutate-r75-session-mv-timeout-gate.py [--keep-going]

它做什么：对每一处「修了就红」的判据做一次**真实**变异，然后：

  ① 变异后跑**目标门**（./bg/ 包），必须**红**；
  ② 红的原因必须是**目标断言**（不是编译错、不是别的门）；
  ③ 还原必须回到**原路径**且**逐字一致**（md5 比对）；
  ④ 备份**不得删除**（基线必须来自独立快照，不能来自「就地还原」）；
  ⑤ 全部跑完后工作区必须零残留。

★ 为什么必须「按原路径 md5 还原」而不是「git checkout」：
  R65 轮出过两次假读数 —— 还原函数把文件拷到仓库根目录而不是原路径，
  于是源文件从未被还原，而「还原校验」读的是同一个错路径 ⇒ 报出假读数。
  ⇒ 这里备份与校验**都**用原路径，且校验读的是**被改的那个文件**。

★ 为什么「编译红」不算通过：
  R65 轮把编译红读成了「门有牙」。本脚本要求输出的失败行里出现**目标测试名**，
  且目标测试名必须以 `--- FAIL:` 开头 ⇒ 编译错与断言错可区分。
"""

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

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

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (编号, 相对路径, 原文本, 变异文本, 期望变红的目标测试名)
MUTATIONS = [
    (
        "M1",
        "bg/materialized_view_refresher.go",
        'sessionViewsStatementTimeout = "600s"',
        'sessionViewsStatementTimeout = "120s"',
        "TestSessionViewsStatementTimeout_ExceedsTheObservedKillLine",
    ),
    (
        "M2",
        "bg/materialized_view_refresher.go",
        'sessionViewsStatementTimeout = "600s"',
        "sessionViewsStatementTimeout = mvRefreshStatementTimeout",
        "TestSessionViewsStatementTimeout_DiffersFromRoutingFamily",
    ),
    (
        "M3",
        "bg/materialized_view_refresher.go",
        "\"SET statement_timeout = '\"+statementTimeout+\"'\"",
        "\"SET statement_timeout = '\"+mvRefreshStatementTimeout+\"'\"",
        "TestRefreshView_UsesThePassedTimeoutNotTheGlobalConstant",
    ),
    (
        "M4",
        "bg/materialized_view_refresher.go",
        "r.refreshView(ctx, view, true, sessionViewsStatementTimeout)",
        "r.refreshView(ctx, view, true, mvRefreshStatementTimeout)",
        "TestRefreshCallSites_PassTheRightFamilyTimeout",
    ),
    (
        "M5",
        "bg/materialized_view_refresher.go",
        "SessionViewsTimeout = 15 * time.Minute",
        "SessionViewsTimeout = 5 * time.Minute",
        "TestSessionViewsTimeout_ExceedsTheFamilyStatementTimeout",
    ),
    (
        "M6",
        "bg/materialized_view_refresher.go",
        '\t"session_client_task_matrix",\n\t"session_task_stats",\n\t"session_client_stats",',
        '\t"session_client_stats",\n\t"session_task_stats",\n\t"session_client_task_matrix",',
        "TestSessionAnalyticsViews_FastestFirst",
    ),
    (
        "M7",
        "bg/materialized_view_refresher.go",
        "sessionViewsPhaseOffset = 5 * time.Minute",
        "sessionViewsPhaseOffset = 0 * time.Minute",
        "TestSessionViewsPhaseOffset_IsNonZeroAndInsideTheInterval",
    ),
    (
        "M8",
        "bg/materialized_view_refresher.go",
        "sessionTimer := time.NewTimer(sessionViewsTickWait(time.Now()))",
        "sessionTimer := time.NewTimer(nextAlignedWait(time.Now(), SessionViewsInterval))",
        "TestRefreshLoop_UsesTheOffsetPhase",
    ),
    (
        "M9",
        "bg/materialized_view_refresher.go",
        "\td := target - phase\n\tif d <= 0 {\n\t\td += interval\n\t}",
        "\td := target - phase\n\tif d < 0 {\n\t\td += interval\n\t}",
        "TestNextAlignedWaitAt_LandsOnTheOffset",
    ),
    (
        "M10",
        "bg/materialized_view_refresher.go",
        "pg_try_advisory_lock($1)`, mvRefreshLockKey)",
        "pg_try_advisory_lock($1)`, int64(12345))",
        "TestNoSecondAdvisoryLockKey",
    ),
    (
        "M11",
        "bg/materialized_view_refresher.go",
        "\tif d < sessionViewsMinTickGap {\n\t\td += SessionViewsInterval\n\t}",
        "\tif false && d < sessionViewsMinTickGap {\n\t\td += SessionViewsInterval\n\t}",
        "TestSessionViewsTickWait_KeepsAGapAfterTheStartupRound",
    ),
]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_gate(test_name):
    env = dict(os.environ)
    env["PATH"] = os.path.expanduser("~/sdk/go/bin") + ":" + env.get("PATH", "")
    env.pop("TEST_DATABASE_URL", None)
    env.pop("TEST_DB_URL", None)
    p = subprocess.run(
        ["go", "test", "./bg/", "-run", test_name, "-count=1"],
        cwd=ROOT, env=env, capture_output=True, text=True,
    )
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep-going", action="store_true")
    args = ap.parse_args()

    failures = []

    # ---- 0. 基线：未变异时目标门必须是绿的 ----
    for name, _rel, _old, _new, test in MUTATIONS:
        rc, out = run_gate(test)
        if rc != 0:
            failures.append(f"基线不绿：{name} 的目标门 {test} 在**未变异**时就红了\n{out}")
            print(f"[基线红] {name} {test}")
        else:
            print(f"[基线绿] {name} {test}")

    backup_dir = tempfile.mkdtemp(prefix="kx-r75-sessmv-mutate-")
    print(f"★ 备份目录（**不删除**，基线必须来自独立快照）：{backup_dir}")

    try:
        for name, rel, old, new, test in MUTATIONS:
            path = os.path.join(ROOT, rel)
            orig_md5 = md5(path)
            with open(path, encoding="utf-8") as f:
                content = f.read()
            if old not in content:
                failures.append(f"{name}: 目标文本不存在于 {rel} ⇒ 变异没生效（门可能恒绿）")
                print(f"[跳过] {name}: 目标文本不存在")
                continue

            # 备份到独立目录（保留到脚本结束，不删）
            bak = os.path.join(backup_dir, name + "__" + os.path.basename(rel))
            shutil.copy2(path, bak)

            mutated = content.replace(old, new, 1)
            with open(path, "w", encoding="utf-8") as f:
                f.write(mutated)

            rc, out = run_gate(test)
            if rc == 0:
                failures.append(f"{name}: 变异后目标门 {test} **仍然绿** ⇒ 门没有牙")
                print(f"[不变红] {name} {test}")
            else:
                # ★ 红的原因必须是目标断言：输出里必须出现该测试名的 FAIL 行
                fail_line = f"--- FAIL: {test}"
                if fail_line not in out:
                    # 编译红不算「门有牙」：编译错与断言错必须可区分
                    is_compile = "build failed" in out or "cannot use" in out or "undefined" in out
                    if is_compile:
                        failures.append(
                            f"{name}: 变红的原因是**编译错**而不是目标断言 ⇒ 不算门有牙\n{out}"
                        )
                        print(f"[编译红，不算通过] {name}")
                    else:
                        failures.append(
                            f"{name}: 变红了，但红的不是目标门（输出里找不到 {fail_line}）⇒ 可能是别的门\n{out}"
                        )
                        print(f"[红因不对] {name}")
                else:
                    print(f"[变红 ✓] {name} {test}")

            # ---- 还原：回【原路径】，再按【原路径】校验 md5 ----
            shutil.copy2(bak, path)
            back_md5 = md5(path)
            if back_md5 != orig_md5:
                failures.append(
                    f"{name}: 还原后 md5 不一致（期望 {orig_md5}，实际 {back_md5}）⇒ 还原失败"
                )
                print(f"[还原不一致] {name}")
            else:
                print(f"[还原逐字一致 ✓] {name}")

            if not args.keep_going and failures:
                break
    finally:
        print(f"★ 备份目录保留在：{backup_dir}")

    # ---- 残留自检：工作区里不应留下任何变异痕迹 ----
    st = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                        capture_output=True, text=True)
    dirty = [l for l in (st.stdout or "").splitlines() if l.strip()]
    print(f"★ 工作区状态（{len(dirty)} 项；本轮预期的改动应当只有那几处）：")
    for l in dirty:
        print("   " + l)

    if failures:
        print("\n=== 变异验证失败 ===")
        for f in failures:
            print("  · " + f)
        return 1
    print("\n=== 变异验证全部通过 ===")
    return 0


if __name__ == "__main__":
    sys.exit(main())
