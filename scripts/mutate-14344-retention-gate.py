#!/usr/bin/env python3
"""§143.44 门的变异脚本 —— 证明门有牙。

本轮新增三族门：
  ① **选项 A（种子签名跳过）**：domains/ursm/v2/persist/writer.go
  ② **usage_facts 留存登记 + 按日 DROP**：bg/partition_manager.go + settings/spec_lifecycle.go
  ③ **830 列驱动**：sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql.skip

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

用法：
    python3 scripts/mutate-14343-retention-shape-gate.py [REPO_ROOT]
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

VERDICT_REL = "admin/retention_shape_verdict.go"
TSV_REL = "scripts/audit/retention_shape_findings.tsv"
PARTMGR_REL = "bg/partition_manager.go"
IMPL_REL = "domains/ursm/v2/persist/retention_partition.go"
RUNBOOK_REL = ("docs/06-deployment/04-runbooks/runbooks/"
               "2026-10-04-ursm-write-amplification-measurement.md")
WRITER_REL = "domains/ursm/v2/persist/writer.go"
SETTINGS_REL = "settings/spec_lifecycle.go"
MIG830_REL = "sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql.skip"

BG = "./bg/"
PERSIST = "./domains/ursm/v2/persist/"

MUTATIONS = [
    # ── ① 选项 A：种子签名判据（两个条件缺一不可）────────────────────────
    ("M1", "种子签名只判 generation==1（会误伤已被真实观测覆盖的种子）",
     WRITER_REL,
     'if atoi64(hash["generation"]) == 1 && atoi64(hash["updated_at_ms"]) == 0 {',
     'if atoi64(hash["generation"]) == 1 {',
     "TestSection14344RealObservationsAreNeverSkipped", PERSIST),

    ("M2", "种子签名只判 updated_at_ms==0（会误伤字段缺失的早期节点）",
     WRITER_REL,
     'if atoi64(hash["generation"]) == 1 && atoi64(hash["updated_at_ms"]) == 0 {',
     'if atoi64(hash["updated_at_ms"]) == 0 {',
     "TestSection14344RealObservationsAreNeverSkipped", PERSIST),

    ("M3", "种子签名判据永不成立（退化种子重新开始落行）",
     WRITER_REL,
     'if atoi64(hash["generation"]) == 1 && atoi64(hash["updated_at_ms"]) == 0 {',
     'if false && atoi64(hash["generation"]) == 1 {',
     "TestSection14344SeedSignatureRowsAreSkipped", PERSIST),

    # ── ② usage_facts 留存登记 ────────────────────────────────────────────
    ("M4", "usage_facts 从 stateTableTTLSpecs() 消失（该族回到无留存）",
     PARTMGR_REL,
     '\t\t{parent: "usage_facts", setting: "lifecycle.usage_facts_ttl_days", fallback: 365, partitionUnit: "day"},\n',
     "",
     "TestSection14344UsageFactsRetentionIsRegistered", BG),

    ("M5", "usage_facts 的 partitionUnit 改成空（退回月度 helper ⇒ 日分区永远不掉）",
     PARTMGR_REL,
     'fallback: 365, partitionUnit: "day"}',
     'fallback: 365}',
     "TestSection14344UsageFactsRetentionIsRegistered", BG),

    ("M6", "usage_facts 的 TTL 从 365 改成 30（与属主拍板值漂移）",
     PARTMGR_REL,
     'fallback: 365, partitionUnit: "day"}',
     'fallback: 30, partitionUnit: "day"}',
     "TestSection14344UsageFactsRetentionIsRegistered", BG),

    ("M7", "settings 目录里 usage_facts_ttl_days 默认值改成 30（两边漂移）",
     SETTINGS_REL,
     'Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 365,',
     'Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 30,',
     "TestSection14344UsageFactsRetentionIsRegistered", BG),

    ("M8", "按日 DROP 加回 IF EXISTS（计数会被虚增）",
     PARTMGR_REL,
     'stmt := `DROP TABLE ` + pgx.Identifier{"public", name}.Sanitize()',
     'stmt := `DROP TABLE IF EXISTS ` + pgx.Identifier{"public", name}.Sanitize()',
     "TestSection14344DailyDropNeverUsesIfExists", BG),

    # ── ③ 830 列驱动 ─────────────────────────────────────────────────────
    ("M9", "830 从 LIKE 列驱动改回硬编码列清单（列漂移缺陷复发）",
     MIG830_REL,
     "    LIKE public.ursm_node_snapshot_min_legacy\n",
     "    snapshot_ts timestamp with time zone NOT NULL,\n",
     "TestSection14344Migration830MustBeColumnDriven", BG),
]


def md5(path):
    return hashlib.md5(open(path, "rb").read()).hexdigest()


def run_go(tree, args):
    env = gocache.env("mutate")
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
        for mid, desc, rel, needle, repl, want_red, pkg in MUTATIONS:
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
            cp = run_go(tree, ["go", "vet", pkg])
            if cp.returncode != 0:
                print(f"❌ {mid}: 变异后编译/vet 失败 ⇒ 读数作废（编译红 ≠ 断言红）")
                print((cp.stdout + cp.stderr)[-1200:])
                results.append((mid, "COMPILE-FAIL", False))
                shutil.copy2(backup, target)
                continue
            tp = run_go(tree, ["go", "test", pkg, "-run", want_red, "-count=1"])
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
