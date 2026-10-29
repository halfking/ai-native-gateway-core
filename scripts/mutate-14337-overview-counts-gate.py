#!/usr/bin/env python3
"""§143.37 / P20 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.36 与远端 LESSONS「共享仓的变异脚本必须用隔离副本」）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，
若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
⇒ 那是「一个活着的缺陷」，不是「一个脏文件」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，
  副本就不是你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律（承 §143.35 / §143.36）：
  · 文件级备份 + md5 逐字校验（禁用 git checkout --）
  · 替换串不得包含 needle 作为子串
  · needle 必须唯一（否则 Contains 型门只保护「至少有一处」⇒ 假绿）
  · ★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）
  · 变异后必须逐字还原

用法：
    python3 scripts/mutate-14337-overview-counts-gate.py [REPO_ROOT]
"""
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

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

QUERIES_REL = "admin/dashboard_board_queries.go"
FALLBACK_REL = "admin/dashboard_board_fallback.go"

# ── 当前（改后）的门控块 ────────────────────────────────────────────────
CURRENT_GUARD_BLOCK = (
    "\terr := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers)\n"
    "\tif err != nil && !IsMissingRelationError(err) {\n"
    "\t\treturn err\n"
    "\t}\n"
    "\tif !overviewCountsNeedsLogFallback(err, *models, *providers) {\n"
    "\t\treturn nil\n"
    "\t}\n"
    "\treturn h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)\n"
)

# ── 判定函数的那一行 ────────────────────────────────────────────────────
CURRENT_DECISION = "\treturn models == 0 || providers == 0\n"

# ── 调用点 1（queryBoardSummary）──
CALLSITE_SUMMARY = (
    "\tvar activeKeys, activeModels, providers int\n"
    "\t_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)\n"
    "\n"
    "\tsuccessRate := 0.0\n"
)

# ── 调用点 2（fallbackBoardSummary）──
CALLSITE_FALLBACK = (
    "\tvar activeKeys, activeModels, providers int\n"
    "\t_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)\n"
    "\n"
    "\tpayload := map[string]any{\n"
)

MUTATIONS = [
    ("M1", "还原成改前的【无条件 fall-through】（删掉门控）",
     QUERIES_REL, CURRENT_GUARD_BLOCK,
     "\tif err := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers); err != nil {\n"
     "\t\tif !IsMissingRelationError(err) {\n"
     "\t\t\treturn err\n"
     "\t\t}\n"
     "\t}\n"
     "\treturn h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)\n",
     "TestSection14337QueryOverviewCountsIsGated"),

    ("M2", "判定写反（!= 0 时反而回退）",
     QUERIES_REL, CURRENT_DECISION,
     "\treturn models != 0 || providers != 0\n",
     "TestSection14337NeedsLogFallbackDecisionTable"),

    ("M3", "只判一个计数（漏掉 providers）",
     QUERIES_REL, CURRENT_DECISION,
     "\treturn models == 0\n",
     "TestSection14337NeedsLogFallbackDecisionTable"),

    ("M4", "破坏门控的前提（回退不再只补 0 ⇒ 跳过就不再是零语义风险）",
     FALLBACK_REL,
     "\tif *models == 0 && logModels > 0 {\n",
     "\tif logModels > 0 {\n",
     "TestSection14337LogFallbackOnlyFillsZeroCounts"),

    ("M5", "把判定挪到回退【之后】（门控形同虚设）",
     QUERIES_REL, CURRENT_GUARD_BLOCK,
     "\terr := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers)\n"
     "\tif err != nil && !IsMissingRelationError(err) {\n"
     "\t\treturn err\n"
     "\t}\n"
     "\tlogErr := h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)\n"
     "\t_ = overviewCountsNeedsLogFallback(err, *models, *providers)\n"
     "\treturn logErr\n",
     "TestSection14337QueryOverviewCountsIsGated"),

    ("M6", "调用点开始【使用】queryOverviewCounts 的错误（错误面从不可观测变成可观测）",
     QUERIES_REL, CALLSITE_SUMMARY,
     "\tvar activeKeys, activeModels, providers int\n"
     "\th.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)\n"
     "\n"
     "\tsuccessRate := 0.0\n",
     "TestSection14337CallSitesDiscardOverviewCountsError"),

    ("M7", "第二个调用点也开始使用错误（只改一处 ⇒ 门必须仍红）",
     QUERIES_REL, CALLSITE_FALLBACK,
     "\tvar activeKeys, activeModels, providers int\n"
     "\th.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)\n"
     "\n"
     "\tpayload := map[string]any{\n",
     "TestSection14337CallSitesDiscardOverviewCountsError"),
]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_go(cwd, args):
    env = gocache.env("mutate")
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True)


def tracked_dirty(repo):
    """返回工作树里【已跟踪文件】的改动列表（未跟踪文件不算）。"""
    p = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"],
                       cwd=repo, capture_output=True, text=True)
    return [l for l in p.stdout.splitlines() if l.strip()]


def main():
    # ── 前置条件：工作树的已跟踪文件必须干净（否则副本不是 HEAD 那棵树）──
    dirty = tracked_dirty(REPO)
    if dirty:
        print("REFUSED: 工作树里【已跟踪文件】有未提交改动 ⇒ 导出的副本不是 HEAD ⇒ 拒绝运行：")
        for d in dirty:
            print("   ", d)
        return 2

    # ── 导出 HEAD 副本（vendor/ 被 git 跟踪 ⇒ 副本可独立编译）────────────
    work = tempfile.mkdtemp(prefix="p20mut-")
    p = subprocess.run(["git", "archive", "HEAD"], cwd=REPO, capture_output=True)
    if p.returncode != 0:
        print("REFUSED: git archive HEAD 失败:", p.stderr.decode()[:400])
        return 2
    tar = os.path.join(work, "head.tar")
    with open(tar, "wb") as f:
        f.write(p.stdout)
    tree = os.path.join(work, "tree")
    os.makedirs(tree, exist_ok=True)
    p = subprocess.run(["tar", "-x", "-C", tree, "-f", tar], capture_output=True, text=True)
    if p.returncode != 0:
        print("REFUSED: 解包失败:", p.stderr[:400])
        return 2

    targets = {}
    for rel in (QUERIES_REL, FALLBACK_REL):
        t = os.path.join(tree, rel)
        if not os.path.exists(t):
            print(f"REFUSED: 副本里没有 {rel}")
            return 2
        backup = os.path.join(work, os.path.basename(rel) + ".bak")
        # ★ 备份必须【真的落盘】（第一版只记了路径没复制 ⇒ 还原步骤 FileNotFoundError）
        shutil.copy2(t, backup)
        targets[rel] = (t, open(t, "rb").read(), md5(t), backup)

    results = []
    try:
        for mid, desc, rel, needle, repl, want_red in MUTATIONS:
            target, orig, orig_md5, backup = targets[rel]
            nb = needle.encode()
            n = orig.count(nb)
            if n != 1:
                print(f"❌ {mid}: needle 在 {rel} 出现 {n} 次（必须恰好 1 次）⇒ 作废")
                results.append((mid, "SKIP-needle", False))
                continue
            if needle in repl:
                print(f"❌ {mid}: 替换串包含 needle 作为子串 ⇒ 作废")
                results.append((mid, "SKIP-substr", False))
                continue

            # 先把**所有**目标文件还原到原样（避免上一个变异残留）
            for r2, (t2, o2, _, _) in targets.items():
                shutil.copy2(os.path.join(work, os.path.basename(r2) + ".bak"), t2)
            shutil.copy2(backup, target)

            open(target, "wb").write(orig.replace(nb, repl.encode(), 1))
            # ★ 编译必须干净（编译红 ≠ 断言红）
            cp = run_go(tree, ["go", "vet", "./admin/"])
            if cp.returncode != 0:
                print(f"❌ {mid}: 变异后编译/vet 失败 ⇒ 读数作废（编译红 ≠ 断言红）")
                print((cp.stdout + cp.stderr)[-1200:])
                results.append((mid, "COMPILE-FAIL", False))
                shutil.copy2(backup, target)
                continue
            tp = run_go(tree, ["go", "test", "./admin/", "-run", want_red, "-count=1"])
            red = tp.returncode != 0
            print(f"{'✅' if red else '❌'} {mid}: {desc}")
            print(f"     目标 {rel}；期望红({want_red}) ⇒ 实测 "
                  f"{'RED(as expected)' if red else 'GREEN(门没牙!)'}")
            if not red:
                print((tp.stdout + tp.stderr)[-1500:])
            results.append((mid, desc, red))
            shutil.copy2(backup, target)
            assert md5(target) == orig_md5, f"{mid} 还原后 md5 不一致"
    finally:
        for rel, (t2, o2, m2, b2) in targets.items():
            shutil.copy2(b2, t2)
        final = {rel: md5(t2) for rel, (t2, _, _, _) in targets.items()}
        shutil.rmtree(work, ignore_errors=True)

    print()
    for rel, (t2, _, m2, _) in targets.items():
        print(f"postflight(副本) {rel}: md5 {final[rel]} == orig {m2} ⇒ "
              f"{'逐字还原 OK' if final[rel] == m2 else '还原失败!'}")

    # ★ 收尾核一次：共享工作树的已跟踪文件未被改动
    still_dirty = tracked_dirty(REPO)
    print(f"共享工作树的已跟踪文件: {'未被改动 OK' if not still_dirty else '被改动! ' + str(still_dirty)}")

    reds = [r for r in results if r[2]]
    print()
    print(f"变异统计: 红 {len(reds)} / 共 {len(results)}")
    ok = (len(reds) == len(results) and len(results) == len(MUTATIONS)
          and not still_dirty
          and all(final[rel] == m2 for rel, (_, _, m2, _) in targets.items()))
    print("VERDICT:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
