#!/usr/bin/env python3
"""§143.42 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.36 / §143.37 / §143.38 / §143.39 与远端 LESSONS
→「共享仓的变异脚本必须用隔离副本」）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，
若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
⇒ 那是「一个活着的缺陷」，不是「一个脏文件」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，
  副本就不是你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律：文件级备份 + md5 逐字校验（禁用 git checkout --）；替换串不得包含 needle
作为子串；needle 必须唯一；★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）。

★★ 本轮的门是**跨语言**的（Go 决策表 + Go 门 + SQL 判定脚本 + TSV 登记册）
⇒ ⇒ **变异必须能打到「脚本 / 登记册」这些非 Go 产物上**
⇒ ★ 只变异 Go 文件的门，会漏掉本轮真正的失守面。
⇒ ⇒ ★★ **本脚本还负责回答一个元问题**：「三条量具纪律到底承不承重？」
   ⇒ M1/M2/M8 打**登记册**（数据侧），M3/M4/M5 打**判定脚本**（量具侧），
   M6/M7 打**决策表**（逻辑侧）⇒ ⇒ **三类产物各至少有一条变异**。

用法：
    python3 scripts/mutate-14342-reclaim-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

VERDICT_REL = "admin/reclaim_verdict.go"
GATE_REL = "admin/section_14342_reclaim_measurement_test.go"
SQL_REL = "scripts/audit/verify_reclaim_candidates.sql"
TSV_REL = "scripts/audit/reclaim_candidates.tsv"

MUTATIONS = [
    # ── 登记册侧（数据）：三条量具纪律的**后果**是否被钉住 ──────────────
    # M1：把一个**有行**对象伪装成空表 ⇒ 决策表重算得 NOT_RECLAIMABLE，
    #   而登记册写着别的 ⇒ 登记册自说自话 ⇒ 红。
    #   ★ 这正是 §143.40 的原始错误形态（把有行对象写成 0 行）。
    ("M1", "登记册把有行对象伪装成空表（★ §143.40 的原始错误形态）",
     TSV_REL,
     "ursm_node_snapshot_min_legacy\t893845504\tNO\tf\tf\tf\t0\tNOT_RECLAIMABLE",
     "ursm_node_snapshot_min_legacy\t893845504\tYES\tf\tf\tf\t0\tNOT_RECLAIMABLE",
     "TestSection14342ReclaimRegistryVerdictsAreDerived"),

    # M2：把「只能保名回收」的对象写成「可 DROP」⇒ 决策表重算不一致 ⇒ 红。
    ("M2", "登记册把保名回收的对象写成可 DROP（★ 会绕过仓内 L1 DROP 闸）",
     TSV_REL,
     "_request_logs_bodies_hot\t304734208\tYES\tf\tf\tf\t1\tRECLAIMABLE_TRUNCATE",
     "_request_logs_bodies_hot\t304734208\tYES\tf\tf\tf\t1\tRECLAIMABLE_DROP",
     "TestSection14342ReclaimRegistryVerdictsAreDerived"),

    # M8：登记册少一行 ⇒ 绝对条数钉住红 ⇒ 逼人重新定性而不是悄悄放过。
    ("M8", "登记册被悄悄删掉一行（★ 候选集变化必须重新定性）",
     TSV_REL,
     "session_censors_2026_09\t29728768\tNO\tt\tt\tf\t0\tNOT_RECLAIMABLE\t-\t非空\n",
     "",
     "TestSection14342ReclaimRegistryVerdictsAreDerived"),

    # ── 判定脚本侧（量具）：三条纪律是否真的被断言 ──────────────────────
    # M3：判空从 EXISTS 退回 count(*) ⇒ 「EXISTS 必须存在」这条断言红。
    ("M3", "判定脚本的判空从 EXISTS 退回 count(*)(⇒ 量具纪律①失守)",
     SQL_REL,
     "EXISTS (SELECT 1 FROM %s LIMIT 1)",
     "count(*) > 0 FROM %s",
     "TestSection14342ReclaimScriptUsesExactMeasurement"),

    # M4：判定脚本里把 n_live_tup 放回**代码**（不是注释）⇒ 禁令断言红。
    #   ★ 替换串**不包含** needle（needle 含两行，替换串在中间插了一行）
    #     ⇒ 才不会被「替换串包含 needle」的纪律作废。
    ("M4", "判定脚本里把 n_live_tup 放回代码（⇒ 量具纪律①失守）",
     SQL_REL,
     "        pg_total_relation_size(c.oid)::bigint AS total_bytes,\n"
     "        pg_relation_size(c.oid)::bigint AS heap_bytes,",
     "        pg_total_relation_size(c.oid)::bigint AS total_bytes,\n"
     "        COALESCE((SELECT s.n_live_tup FROM pg_stat_user_tables s WHERE s.relid = c.oid), 0) AS n_live_tup,\n"
     "        pg_relation_size(c.oid)::bigint AS heap_bytes,",
     "TestSection14342ReclaimScriptUsesExactMeasurement"),

    # M5：判定脚本里混进破坏性语句 ⇒ 只读性断言红。
    ("M5", "判定脚本里混进破坏性语句（⇒ 它不再是只读的）",
     SQL_REL,
     "    EXECUTE v_q INTO v_has;",
     "    EXECUTE 'TRUNCATE TABLE pg_temp.reclaim_probe';",
     "TestSection14342ReclaimScriptUsesExactMeasurement"),

    # ── 决策表侧（逻辑）─────────────────────────────────────────────────
    # M6：★ 去掉 relispartition 条件 ⇒ 普通表会被判成可 DROP
    #   ⇒ 用例⑧ 翻红 ⇒ 证明「relispartition 条件确实承重」。
    ("M6", "决策表去掉 relispartition 条件（★ 普通表会被误判成可 DROP）",
     VERDICT_REL,
     "case f.RelIsPartition && !f.HasParent:",
     "case !f.HasParent:",
     "TestSection14342ReclaimVerdictDecisionTable"),

    # M7：★ 去掉「有行 ⇒ 不可回收」这条 ⇒ 有行对象会被判成可回收
    #   ⇒ 用例① 翻红 ⇒ 证明「空判是承重的」。
    ("M7", "决策表去掉「有行 ⇒ 不可回收」这条（★ 会销毁数据）",
     VERDICT_REL,
     "case !f.Empty:",
     "case false:",
     "TestSection14342ReclaimVerdictDecisionTable"),
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

    work = tempfile.mkdtemp(prefix="p14342mut-")
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
            print(f"REFUSED: 副本里没有 {rel}（HEAD 里还没有它 ⇒ 先提交再跑变异）")
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
                print(f"❌ {mid}: needle 在 {rel} 出现 {n} 次（必须恰好 1 次）⇒ 作废")
                results.append((mid, "SKIP-needle-count", False))
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
