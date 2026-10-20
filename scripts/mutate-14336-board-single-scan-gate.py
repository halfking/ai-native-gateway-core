#!/usr/bin/env python3
"""§143.36 / P12 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（2026-10-21 补，承远端 LESSONS「共享仓的变异脚本必须用隔离副本」）：
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
    python3 scripts/mutate-14336-board-single-scan-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

TARGET_REL = "admin/dashboard_board_fallback.go"

# 现状（改后）的整块：SQL 模板 + fmt.Sprintf 实参
CURRENT_SQL_BLOCK = (
    "\terr := h.db.QueryRow(ctx, fmt.Sprintf(`\n"
    "\t\tSELECT COUNT(DISTINCT %s), COUNT(DISTINCT %s.provider_id)\n"
    "\t\tFROM %s\n"
    "\t\tWHERE %s\n"
    "\t`, modelExpr, alias, logsTable, where), args...).Scan(&logModels, &logProviders)\n"
)

MUTATIONS = [
    ("M1", "退化回双子查询（单遍 → 两个标量子查询 ⇒ 派生表被扫两遍）",
     CURRENT_SQL_BLOCK,
     "\terr := h.db.QueryRow(ctx, fmt.Sprintf(`\n"
     "\t\tSELECT\n"
     "\t\t\t(SELECT COUNT(DISTINCT %s) FROM %s WHERE %s AND %s IS NOT NULL),\n"
     "\t\t\t(SELECT COUNT(DISTINCT %s.provider_id) FROM %s WHERE %s AND %s.provider_id IS NOT NULL)\n"
     "\t`, modelExpr, logsTable, where, modelExpr, alias, logsTable, where, alias), args...).Scan(&logModels, &logProviders)\n",
     "TestSection14336OverviewCountsIsSingleScan|TestSection14336NoScalarSubqueryInOverviewCounts"),

    ("M2", "把聚合换成非 DISTINCT 形态（COUNT(*) —— 对 NULL 敏感，免检许可失效）",
     "\t\tSELECT COUNT(DISTINCT %s), COUNT(DISTINCT %s.provider_id)\n"
     "\t\tFROM %s\n"
     "\t\tWHERE %s\n"
     "\t`, modelExpr, alias, logsTable, where), args...).Scan(&logModels, &logProviders)\n",
     "\t\tSELECT COUNT(*), COUNT(DISTINCT %s.provider_id)\n"
     "\t\tFROM %s\n"
     "\t\tWHERE %s AND (%s IS NOT NULL OR %s IS NULL)\n"
     "\t`, alias, logsTable, where, modelExpr, modelExpr), args...).Scan(&logModels, &logProviders)\n",
     "TestSection14336DistinctAggregatesIgnoreNulls"),

    ("M3", "换掉完整投影 helper（走向「裁剪投影」这条实测已排除的路）",
     "func (h *Handler) fillOverviewCountsFromLogs(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {\n"
     "\tlogsTable, alias := boardRequestLogsFromClause()\n",
     "func (h *Handler) fillOverviewCountsFromLogs(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {\n"
     "\tlogsTable, alias := \"(SELECT 1 AS ts)\", \"r\"\n",
     "TestSection14336NarrowProjectionIsNotTheFix"),

    ("M4", "在单遍 SQL 里让派生表被第二次引用（伪装成单遍但实际两遍）",
     "\t\tWHERE %s\n"
     "\t`, modelExpr, alias, logsTable, where), args...).Scan(&logModels, &logProviders)\n",
     "\t\tWHERE %s\n"
     "\t\tAND (SELECT COUNT(*) FROM %s) >= 0\n"
     "\t`, modelExpr, alias, logsTable, where, logsTable), args...).Scan(&logModels, &logProviders)\n",
     "TestSection14336OverviewCountsIsSingleScan"),

    # ★★ M5 的设计要点（第一版做反了 ⇒ 门绿 ⇒ 读数作废）：
    #   第一版把「改前形态」【追加】到注释里 ⇒ 门仍绿 —— 这是【正确】行为：
    #   剥注释后源码里没有那个形态 ⇒ 门当然不该红。
    #   ★ 正确的 M5 = 把【真正的代码】删掉、只留注释里的形态
    #   ⇒ 剥注释后源码里没有形态 ⇒ 门【必须红】⇒ 证明剥注释层【承重】
    #   （若门仍绿 ⇒ 说明它在读未剥注释的文本 ⇒ 门可被注释满足 ⇒ 无牙）。
    ("M5", "把改前形态从代码里删掉、只留在注释里（★ 证明剥注释层承重）",
     CURRENT_SQL_BLOCK,
     "\t// (SELECT COUNT(DISTINCT %s) FROM %s WHERE %s AND %s IS NOT NULL),\n"
     "\t// (SELECT COUNT(DISTINCT %s.provider_id) FROM %s WHERE %s AND %s.provider_id IS NOT NULL)\n"
     "\t_ = logsTable\n"
     "\t_ = where\n"
     "\terr := h.db.QueryRow(ctx, fmt.Sprintf(`\n"
     "\t\tSELECT %s, %s\n"
     "\t`, modelExpr, alias), args...).Scan(&logModels, &logProviders)\n",
     "TestSection14336OverviewCountsIsSingleScan|TestSection14336NoScalarSubqueryInOverviewCounts"),
]

TEST_RE = "TestSection14336"


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_go(cwd, args):
    env = dict(os.environ)
    env["GOCACHE"] = "/tmp/gocache-p1437"
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
    work = tempfile.mkdtemp(prefix="p12mut-")
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

    target = os.path.join(tree, TARGET_REL)
    if not os.path.exists(target):
        print(f"REFUSED: 副本里没有 {TARGET_REL}")
        return 2

    orig = open(target, "rb").read()
    orig_md5 = md5(target)
    backup = os.path.join(work, "fallback.go.bak")
    shutil.copy2(target, backup)

    results = []
    try:
        for mid, desc, needle, repl, want_red in MUTATIONS:
            nb = needle.encode()
            n = orig.count(nb)
            if n != 1:
                print(f"❌ {mid}: needle 出现 {n} 次（必须恰好 1 次）⇒ 作废")
                results.append((mid, "SKIP-needle", False))
                continue
            if needle in repl:
                print(f"❌ {mid}: 替换串包含 needle 作为子串 ⇒ 作废")
                results.append((mid, "SKIP-substr", False))
                continue
            open(target, "wb").write(orig.replace(nb, repl.encode(), 1))
            # ★ 编译必须干净（编译红 ≠ 断言红）
            cp = run_go(tree, ["go", "vet", "./admin/"])
            if cp.returncode != 0:
                print(f"❌ {mid}: 变异后编译/vet 失败 ⇒ 读数作废（编译红 ≠ 断言红）")
                print((cp.stdout + cp.stderr)[-1000:])
                results.append((mid, "COMPILE-FAIL", False))
                shutil.copy2(backup, target)
                continue
            tp = run_go(tree, ["go", "test", "./admin/", "-run", want_red, "-count=1"])
            red = tp.returncode != 0
            print(f"{'✅' if red else '❌'} {mid}: {desc}")
            print(f"     期望红 ⇒ 实测 {'RED(as expected)' if red else 'GREEN(门没牙!)'}")
            if not red:
                print((tp.stdout + tp.stderr)[-1200:])
            results.append((mid, desc, red))
            shutil.copy2(backup, target)
            assert md5(target) == orig_md5, f"{mid} 还原后 md5 不一致"
    finally:
        shutil.copy2(backup, target)
        final_md5 = md5(target)
        shutil.rmtree(work, ignore_errors=True)

    print()
    print(f"postflight(副本): md5 {final_md5} == orig {orig_md5} ⇒ "
          f"{'逐字还原 OK' if final_md5 == orig_md5 else '还原失败!'}")

    # ★ 收尾核一次：共享工作树的已跟踪文件未被改动
    still_dirty = tracked_dirty(REPO)
    print(f"共享工作树的已跟踪文件: {'未被改动 OK' if not still_dirty else '被改动! ' + str(still_dirty)}")

    reds = [r for r in results if r[2]]
    print()
    print(f"变异统计: 红 {len(reds)} / 共 {len(results)}")
    ok = len(reds) == len(results) and final_md5 == orig_md5 and not still_dirty
    print("VERDICT:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
