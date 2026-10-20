#!/usr/bin/env python3
"""§143.38 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.36 / §143.37 与远端 LESSONS「共享仓的变异脚本必须用隔离副本」）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，
若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
⇒ 那是「一个活着的缺陷」，不是「一个脏文件」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，
  副本就不是你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律：文件级备份 + md5 逐字校验（禁用 git checkout --）；替换串不得包含 needle
作为子串；needle 必须唯一；★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）。

用法：
    python3 scripts/mutate-14338-retirement-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

LIFECYCLE_REL = "admin/data_lifecycle_partition.go"
QUERIES_REL = "admin/dashboard_board_queries.go"
PRECHECK_REL = "scripts/audit/verify_890_cutover_precheck.sql"
RUNBOOK_REL = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

MUTATIONS = [
    ("M1", "抹掉 _request_logs 的【退役登记】（退役链在 Go 侧变成隐式）",
     LIFECYCLE_REL,
     '\t\tHasArchiveFunc:   false,\n'
     '\t\t// ★ v1 族在迁移 890 被 DROP（§143.38）⇒ 登记退役归属，\n'
     '\t\t//   让门能断言「本清单与 890 的 DROP 清单一致」。\n'
     '\t\tRetiredByMigration: "890",\n',
     '\t\tHasArchiveFunc:   false,\n',
     "TestSection14338ManagedTableListMatchesDropList"),

    ("M2", "把退役登记写成别的迁移号",
     LIFECYCLE_REL,
     '\t\tRetiredByMigration: "890",\n',
     '\t\tRetiredByMigration: "891",\n',
     "TestSection14338ManagedTableListMatchesDropList"),

    ("M3", "在仓内加一处【真读点】：一条引用 v1 表的 SQL 字面量",
     QUERIES_REL,
     "func (h *Handler) queryOverviewCountsMinute(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {\n",
     "var mut14338V1ReadPoint = \"SELECT 1 FROM _request_logs_archive\"\n"
     "\n"
     "func (h *Handler) queryOverviewCountsMinute(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {\n",
     "TestSection14338NoGoSQLLiteralReadsADroppedV1Table"),

    ("M4", "核验脚本删掉判据本身（still_references_v1 ⇒ 换个说法）",
     PRECHECK_REL,
     "still_references_v1",
     "still_touches_v1",
     "TestSection14338CutoverPrecheckScriptExists"),

    ("M5", "核验脚本里混进真 DDL（⇒ 它不再是只读，生产护栏会拒）",
     PRECHECK_REL,
     "SET statement_timeout = '60s';",
     "SET statement_timeout = '60s';\nDROP TABLE IF EXISTS public._request_logs;",
     "TestSection14338CutoverPrecheckScriptExists"),

    ("M6", "抹掉 runbook 里的关键提交号（本节在案证据被改掉）",
     RUNBOOK_REL,
     "5f64031cb",
     "5f64031cd",
     "TestSection14338RunbookHasSection"),
]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_go(cwd, args):
    env = dict(os.environ)
    env["GOCACHE"] = "/tmp/gocache-p1437"
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True)


def tracked_dirty(repo):
    p = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"],
                       cwd=repo, capture_output=True, text=True)
    return [l for l in p.stdout.splitlines() if l.strip()]


def main():
    dirty = tracked_dirty(REPO)
    if dirty:
        print("REFUSED: 工作树里【已跟踪文件】有未提交改动 ⇒ 导出的副本不是 HEAD ⇒ 拒绝运行：")
        for d in dirty:
            print("   ", d)
        return 2

    work = tempfile.mkdtemp(prefix="p890mut-")
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

    rels = sorted({m[2] for m in MUTATIONS})
    targets = {}
    for rel in rels:
        t = os.path.join(tree, rel)
        if not os.path.exists(t):
            print(f"REFUSED: 副本里没有 {rel}")
            return 2
        backup = os.path.join(work, rel.replace("/", "_") + ".bak")
        shutil.copy2(t, backup)   # ★ 备份必须真的落盘（第一版只记路径没复制）
        targets[rel] = (t, open(t, "rb").read(), md5(t), backup)

    results = []
    try:
        for mid, desc, rel, needle, repl, want_red in MUTATIONS:
            target, orig, orig_md5, backup = targets[rel]
            nb = needle.encode()
            n = orig.count(nb)
            if n < 1:
                print(f"❌ {mid}: needle 在 {rel} 出现 {n} 次（必须 ≥1 次）⇒ 作废")
                results.append((mid, "SKIP-needle", False))
                continue
            if needle in repl:
                print(f"❌ {mid}: 替换串包含 needle 作为子串 ⇒ 作废")
                results.append((mid, "SKIP-substr", False))
                continue

            # 先把**所有**目标文件还原（避免上一个变异残留）
            for r2, (t2, _, _, b2) in targets.items():
                shutil.copy2(b2, t2)
            shutil.copy2(backup, target)

            open(target, "wb").write(orig.replace(nb, repl.encode()))
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
            print(f"     目标 {rel}（needle x{n}）；期望红({want_red}) ⇒ 实测 "
                  f"{'RED(as expected)' if red else 'GREEN(门没牙!)'}")
            if not red:
                print((tp.stdout + tp.stderr)[-1500:])
            results.append((mid, desc, red))
            shutil.copy2(backup, target)
            assert md5(target) == orig_md5, f"{mid} 还原后 md5 不一致"
    finally:
        for rel, (t2, _, _, b2) in targets.items():
            shutil.copy2(b2, t2)
        final = {rel: md5(t2) for rel, (t2, _, _, _) in targets.items()}
        shutil.rmtree(work, ignore_errors=True)

    print()
    for rel, (t2, _, m2, _) in targets.items():
        print(f"postflight(副本) {rel}: md5 {final[rel]} == orig {m2} ⇒ "
              f"{'逐字还原 OK' if final[rel] == m2 else '还原失败!'}")

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
