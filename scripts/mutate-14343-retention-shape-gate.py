#!/usr/bin/env python3
"""§143.43 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.42 脚本与共享仓 LESSONS）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，若并行会话恰好
`git commit -a` ⇒ **被故意改坏的那一行会被推上 main** ⇒ 那是「一个活着的缺陷」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，副本就不是
  你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律：文件级备份 + md5 逐字校验（禁用 git checkout --）；替换串不得包含 needle
作为子串；needle 必须唯一；★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）。

★★ 本轮的门跨四类产物 ⇒ ⇒ **每类至少一条变异**，否则「没被变异覆盖的面」就是未知：
  ① 决策表（逻辑侧）→ M1 / M2
  ② 登记册（数据侧）→ M3
  ③ 缺口打桩的**被钉住对象**（机制侧）→ M4
  ④ 证据字符串（实现 + 文档侧）→ M5 / M6 / M7
  ⑤ §143.45 的双向钉与新口径 → M8 / M9 / M10 / M11

★★ §143.45 更新（M3 重指 / M4 反向 / 新增 M8~M11）：
   ★ **门改了 ⇒ 变异必须跟着改**；否则「变异全绿」只是**变异没打在新判据上**
     —— 例如 M4 原意是「把 usage_facts 填进注册表」，而 §143.44 已经填了 ⇒
     该变异退化成「加一条重复登记」⇒ 门当然不红 ⇒ 那不是门没牙，是变异失效。

用法：
    python3 scripts/mutate-14343-retention-shape-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

VERDICT_REL = "admin/retention_shape_verdict.go"
TSV_REL = "scripts/audit/retention_shape_findings.tsv"
PARTMGR_REL = "bg/partition_manager.go"
IMPL_REL = "domains/ursm/v2/persist/retention_partition.go"
RUNBOOK_REL = ("docs/06-deployment/04-runbooks/runbooks/"
               "2026-10-04-ursm-write-amplification-measurement.md")

MUTATIONS = [
    # ── ① 决策表（逻辑侧）────────────────────────────────────────────────
    # M1：删掉「回滚锚点优先于『0 行 ⇒ 可回收』」这条语义分支。
    #   ★ 这是本轮唯一承重的**语义**分支：锚点即使被清空也不能删
    #     （删它就断了 830 的唯一退路）⇒ 删掉分支 ⇒ 自测的承重例必须红。
    ("M1", "决策表删掉「回滚锚点优先」分支（0 行锚点会被判成可回收）",
     VERDICT_REL,
     "\tif in.IsRollbackAnchor {\n"
     "\t\treturn VerdictNotReclaimable, \"回滚锚点：删它就断了该迁移的唯一退路\"\n"
     "\t}\n\n",
     "",
     "TestSection14343DecisionTableSelfTest"),

    # M2：把分区形态的判据改掉（'p' → 'P'）⇒ 252 那条结论的依据被抽掉。
    ("M2", "决策表不再认 'p' 为分区父表（252=DROP 的判据被抽掉）",
     VERDICT_REL,
     "\tcase \"p\":\n",
     "\tcase \"P\":\n",
     "TestSection14343DecisionTableSelfTest"),

    # ── ② 登记册（数据侧）────────────────────────────────────────────────
    # M3：把 211 快照族的关键字节数 +1 ⇒ 绝对字节钉住必须红。
    #   ★ 刻意改 verdict **不**能红的那一位：判定结果不变，只有「钉住的绝对读数」能发现它
    #     ⇒ 这正是「绝对读数钉住」存在的理由。
    #   ★ §143.45：needle 已重指到 post-§143.44 的读数（原值 6042257/2411266048
    #     已被本轮生产写入改变 ⇒ 那条 needle 在刷新后的登记册里**不存在**
    #     ⇒ 脚本会按纪律「SKIP-needle-count」作废该轮 ⇒ 变异统计失真）。
    ("M3", "登记册的关键字节数被改动（verdict 仍自洽 ⇒ 只能靠绝对读数钉住发现）",
     TSV_REL,
     "6083147\t2029412736\t0",
     "6083147\t2029412737\t0",
     "TestSection14343RegistryIsRecomputable"),

    # ── ③ 缺口打桩的**被钉住对象**（机制侧）────────────────────────────
    # M4：★ §143.45 **反向**：把 stateTableTTLSpecs() 里的 usage_facts 登记**改名**。
    #   ★ 为什么要反向：§143.44 已经把 usage_facts 登记进去了 ⇒ 原变异
    #     （「加一条 usage_facts」）现在只是「加一条重复登记」⇒ 门不会红
    #     ⇒ 那不叫「门没牙」，那叫**变异没打在新判据上**。
    #   ★ 改名成 usage_facts_x 而不是删行：改名是**纯语义**变化
    #     （父表名不再精确等于 usage_facts ⇒ 该族回到无留存）⇒ 仓内那根钉必须红。
    ("M4", "usage_facts 的仓内登记被改名（仓内那侧闭合被破坏 ⇒ 门必须红）",
     PARTMGR_REL,
     "\t\t{parent: \"usage_facts\", setting: \"lifecycle.usage_facts_ttl_days\", fallback: 365, partitionUnit: \"day\"},\n",
     "\t\t{parent: \"usage_facts_x\", setting: \"lifecycle.usage_facts_x_ttl_days\", fallback: 365, partitionUnit: \"day\"},\n",
     "TestSection14343UsageFactsGapIsOpenInProductionAndClosedInRepo"),

    # ── ④ 证据字符串（实现 + 文档侧）────────────────────────────────────
    # M5：实现里的 row-delete 常量被改名 ⇒ 「211 走 row-delete」这条证据不可复现。
    ("M5", "实现里的 mode 常量 row-delete 被改名（211 的证据字符串消失）",
     IMPL_REL,
     "RetentionModeRowDelete     = \"row-delete\"",
     "RetentionModeRowDelete     = \"rd\"",
     "TestSection14343EvidenceStringsArePinned"),

    # M6：runbook 里的订正横幅被删 ⇒ 订正的书面载体消失。
    ("M6", "runbook 的订正横幅二十二被删（结论的书面载体消失）",
     RUNBOOK_REL,
     "> 🚨🚨🚨 **订正横幅二十二（§143.43）**",
     "> 🚨🚨🚨 **（横幅已被删除）**",
     "TestSection14343EvidenceStringsArePinned"),

    # M7：runbook 里「上一轮三处判词」的原文保留块被删 ⇒ 订正失去对照面。
    ("M7", "runbook 里被推翻的原判词块被删（订正失去对照 ⇒ 读者看不出改了什么）",
     RUNBOOK_REL,
     "#### §143.43.1 上一轮的三处判词（**逐字保留**，供对照）",
     "#### §143.43.1 （原文保留块已被删除）",
     "TestSection14343EvidenceStringsArePinned"),
    # ── ⑤ §143.45 新增：双向钉的另外几根 + 新口径 ─────────────────────────
    # M8：把登记册里 252 usage_facts 族的 ttl_registered 改成 yes（并把 verdict
    #   同步改成 PARTITION_DROP_ACTIVE，使**决策表重算仍然自洽**）
    #   ⇒ 只能靠「生产侧那根钉」发现 ⇒ 证明它不是恒绿门。
    ("M8", "登记册谎报生产已闭合（ttl_registered=yes 且 verdict 自洽 ⇒ 靠生产侧钉发现）",
     TSV_REL,
     "276190\t254402560\t0\tno\tGAP_NO_RETENTION\tTTL_DECIDED_PENDING_DEPLOY",
     "276190\t254402560\t0\tyes\tPARTITION_DROP_ACTIVE\tTTL_DECIDED_PENDING_DEPLOY",
     "TestSection14343UsageFactsGapIsOpenInProductionAndClosedInRepo"),

    # M9：把 method 漂回「还没拍板」（TTL_DECIDED_PENDING_DEPLOY → TTL_DECISION_NEEDED）
    #   ★ 只动 211 那一行（needle 唯一）：两行都写同一个值 ⇒ needle 不唯一会被作废。
    ("M9", "登记册的 method 漂回「还没拍板」（已拍板待部署 vs 还没拍板 必须可区分）",
     TSV_REL,
     "88883\t80232448\t0\tno\tGAP_NO_RETENTION\tTTL_DECIDED_PENDING_DEPLOY",
     "88883\t80232448\t0\tno\tGAP_NO_RETENTION\tTTL_DECISION_NEEDED",
     "TestSection14343UsageFactsGapIsOpenInProductionAndClosedInRepo"),

    # M10：把分区父表族的族总量写成 0 —— 正是 `pg_total_relation_size(父表)=0`
    #   那个量具坑的形状 ⇒ 「族总量必须 > 0」这根钉必须红。
    #   ⚠ 它也会触发绝对字节钉（同一行）⇒ 双红属正常，脚本只看"是否红"。
    ("M10", "分区父表族的族总量被写成 0（抄了父表函数的返回值 ⇒ 量具坑复发）",
     TSV_REL,
     "29262511\t9226485760\t0",
     "29262511\t0\t0",
     "TestSection14343RegistryIsRecomputable"),

    # M11：把 §143.45 的订正横幅二十三删掉 ⇒ 结论的书面载体消失。
    ("M11", "runbook 的订正横幅二十三被删（§143.45 的书面载体消失）",
     RUNBOOK_REL,
     "> 🚨🚨🚨 **订正横幅二十三（§143.45 追加 · 订正 §143.43 的三处「现状」）**",
     "> 🚨🚨🚨 **（横幅已被删除）**",
     "TestSection14343EvidenceStringsArePinned"),
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
        print("REFUSED: 工作树里【已跟踪文件】有未提交改动 ⇒ 导出的副本不是 HEAD ⇒ 拒绝运行：")
        for d in dirty:
            print("   ", d)
        return 2

    work = tempfile.mkdtemp(prefix="p14343mut-")
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
            # ★ vet 覆盖被变异文件所在包：admin/（门与决策表）与 bg/（M4 改的注册表）
            #   —— 只 vet ./admin/ 时，M4 那条变异根本不会被编译检查到。
            cp = run_go(tree, ["go", "vet", "./admin/", "./bg/"])
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
