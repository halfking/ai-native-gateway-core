#!/usr/bin/env python3
"""§143.46 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.42/§143.43 脚本与共享仓 LESSONS）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，若并行会话恰好
`git commit -a` ⇒ **被故意改坏的那一行会被推上 main** ⇒ 那是「一个活着的缺陷」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，副本就不是
  你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律：文件级备份 + md5 逐字校验（禁用 git checkout --）；替换串不得包含 needle
作为子串；needle 必须唯一；★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）。

★ 本轮的门跨四类产物 ⇒ ⇒ **每类至少一条变异**：
  ① 决策表（覆盖判定 + 过期判定 + 跨月差）→ M1 / M2 / M3
  ② 过期口径三处对齐（841 SQL / 判定脚本）→ M4 / M5 / M6 / M7
  ③ 登记册（事实层）→ M8
  ④ 机制不许被拆（841 迁移 / Go 调用）→ M9 / M10

用法：
    python3 scripts/mutate-14346-monthly-retention-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

VERDICT_REL = "admin/monthly_retention_verdict.go"
TSV_REL = "scripts/audit/monthly_partition_retention.tsv"
SQL_REL = "scripts/audit/verify_monthly_partition_retention.sql"
MIG_REL = "sql/migrations/startup/841_monthly_partition_retention.sql"
PM_REL = "bg/partition_manager.go"

MUTATIONS = [
    # ── ① 决策表 ────────────────────────────────────────────────────────
    # M1：把「无机制 ⇒ 无界」判反 ⇒ 覆盖判定失守。
    ("M1", "决策表把「无任何机制」判成已覆盖（无界族被放过）",
     VERDICT_REL,
     "\tif in.Mechanism == MonthlyMechNone {\n",
     "\tif false {\n",
     "TestSection14346DecisionTableSelfTest"),

    # M2：★ 承重 —— 把过期方向写反 ⇒ 当月/未来月会被判为过期。
    ("M2", "过期方向写反（当月与未来月会被判过期 ⇒ 写入路径 23514）",
     VERDICT_REL,
     "\treturn partMonth.Before(cutoff)\n",
     "\treturn !partMonth.Before(cutoff)\n",
     "TestSection14346DecisionTableSelfTest"),

    # M3：跨月差退化成「恒 0」⇒ 正是本轮在 SQL 侧踩过的那个坑。
    ("M3", "跨月差退化成恒 0（timestamp-date 那类 interval 的 EXTRACT(month) 恒 0）",
     VERDICT_REL,
     "\treturn (cy*12 + cm) - (py*12 + pm)\n",
     "\treturn 0\n",
     "TestSection14346DecisionTableSelfTest"),

    # ── ② 过期口径三处对齐 ─────────────────────────────────────────────
    # M4：841 的严格小于被改成 >=（当月/未来月会被 DROP）。
    ("M4", "841 的过期比较被改成 >=（当月与预建分区会被 DROP）",
     MIG_REL,
     "IF mth < cutoff THEN",
     "IF mth >= cutoff THEN",
     "TestSection14346ExpirySemanticsAlignAcrossThreePlaces"),

    # M5：841 的 cutoff 月算被改掉（过期线不再是「当月月初往前推 N-1 个月」）。
    ("M5", "841 的 cutoff 月算被改掉（过期线漂移）",
     MIG_REL,
     "cutoff := cur_month - ((cfg.retain_months - 1) || ' months')::interval;",
     "cutoff := cur_month - ((cfg.retain_months - 1) || ' days')::interval;",
     "TestSection14346ExpirySemanticsAlignAcrossThreePlaces"),

    # M6：★ 判定脚本的过期方向写反 ⇒ 数字变成「保留越多回收越多」。
    ("M6", "判定脚本的过期方向写反（保留越多回收越多）",
     SQL_REL,
     "count(*) FILTER (WHERE months_old >= n_keep_months) AS droppable_parts",
     "count(*) FILTER (WHERE months_old < n_keep_months) AS droppable_parts",
     "TestSection14346ExpirySemanticsAlignAcrossThreePlaces"),

    # M7：★ 判定脚本恢复 timestamp-date 那类写法 ⇒ 跨月差恒 0。
    ("M7", "判定脚本用 EXTRACT(month FROM (timestamp - date)) 算跨月差（恒 0）",
     SQL_REL,
     "((EXTRACT(year FROM date_trunc('month', now()))::int * 12 + EXTRACT(month FROM date_trunc('month', now()))::int)",
     "((EXTRACT(year FROM (date_trunc('month', now()) - to_date('2026-01-01','YYYY-MM-DD'))) * 12 + EXTRACT(month FROM (date_trunc('month', now()) - to_date('2026-01-01','YYYY-MM-DD'))))",
     "TestSection14346ExpirySemanticsAlignAcrossThreePlaces"),

    # ── ③ 登记册 ───────────────────────────────────────────────────────
    # M8：把 session_turns 的 mechanism 改成「已被 TTL 覆盖」⇒ 谎报有留存。
    ("M8", "登记册谎报 session_turns 已被 TTL 覆盖（把无界族写成有留存）",
     TSV_REL,
     "252\tMONTHLY_FAMILY\tsession_turns\t5\t4497375232\t2868215808\t442368\t221184\tno\t-1\tNONE\tUNBOUNDED",
     "252\tMONTHLY_FAMILY\tsession_turns\t5\t4497375232\t2868215808\t442368\t221184\tno\t30\tSTATE_TABLE_TTL\tCOVERED",
     "TestSection14346RegistryIsRecomputable"),

    # ── ④ 机制不许被拆 ─────────────────────────────────────────────────
    # M9：841 的配置表定义被删 ⇒ 路径 B 不存在了。
    ("M9", "841 的配置表定义被删（路径 B 机制被拆）",
     MIG_REL,
     "CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_retention (",
     "CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_retention_disabled (",
     "TestSection14346MechanismIsInPlace"),

    # M10：Go 侧不再调用 DROP 函数 ⇒ 机制建好了也没人跑。
    ("M10", "Go 侧不再调用 llm_gateway_drop_expired_month_partitions（机制没人跑）",
     PM_REL,
     "SELECT llm_gateway_drop_expired_month_partitions()",
     "SELECT 0",
     "TestSection14346MechanismIsInPlace"),
]


def md5(path):
    return hashlib.md5(open(path, "rb").read()).hexdigest()


def run_go(tree, args):
    env = dict(os.environ)
    env["GOCACHE"] = "/tmp/gocache-p1437"
    return subprocess.run(args, cwd=tree, env=env, capture_output=True, text=True)


def tracked_dirty(repo):
    p = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"],
                       cwd=repo, capture_output=True, text=True)
    return [l for l in p.stdout.splitlines() if l.strip()]


def main():
    dirty = tracked_dirty(REPO)
    if dirty:
        print("REFUSED: 工作树里【已跟踪文件】有未提交改动 => 导出的副本不是 HEAD => 拒绝运行：")
        for d in dirty:
            print("   ", d)
        return 2

    work = tempfile.mkdtemp(prefix="p14346mut-")
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
            print(f"REFUSED: 副本里没有 {rel}（HEAD 里还没有它 => 先提交再跑变异）")
            return 2
        backup = os.path.join(work, rel.replace("/", "_") + ".bak")
        shutil.copy2(t, backup)   # ★ 备份必须真的落盘
        targets[rel] = (t, open(t, "rb").read(), md5(t), backup)

    results = []
    try:
        for mid, desc, rel, needle, repl, want_red in MUTATIONS:
            target, orig, orig_md5, backup = targets[rel]
            nb = needle.encode()
            n = orig.count(nb)
            if n != 1:
                print(f"❌ {mid}: needle 在 {rel} 出现 {n} 次（必须恰好 1 次）=> 作废")
                results.append((mid, "SKIP-needle-count", False))
                continue
            if needle in repl:
                print(f"❌ {mid}: 替换串包含 needle 作为子串 => 作废")
                results.append((mid, "SKIP-substr", False))
                continue

            # 先把**所有**目标文件还原（避免上一个变异残留）
            for r2, (t2, _, _, b2) in targets.items():
                shutil.copy2(b2, t2)
            shutil.copy2(backup, target)

            open(target, "wb").write(orig.replace(nb, repl.encode()))
            cp = run_go(tree, ["go", "vet", "./admin/", "./bg/"])
            if cp.returncode != 0:
                print(f"❌ {mid}: 变异后编译/vet 失败 => 读数作废（编译红 ≠ 断言红）")
                print((cp.stdout + cp.stderr)[-1200:])
                results.append((mid, "COMPILE-FAIL", False))
                shutil.copy2(backup, target)
                continue
            tp = run_go(tree, ["go", "test", "./admin/", "-run", want_red, "-count=1"])
            red = tp.returncode != 0
            print(f"{'✅' if red else '❌'} {mid}: {desc}")
            print(f"     目标 {rel}（needle x{n}）；期望红({want_red}) => 实测 "
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
        print(f"postflight(副本) {rel}: md5 {final[rel]} == orig {m2} => "
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
