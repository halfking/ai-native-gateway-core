#!/usr/bin/env python3
"""§143.39 门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.36 / §143.37 / §143.38 与远端 LESSONS
「共享仓的变异脚本必须用隔离副本」）：
本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，
若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
⇒ 那是「一个活着的缺陷」，不是「一个脏文件」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

★ 前置条件：副本来自 **HEAD** ⇒ 工作树里**已跟踪文件**若有未提交改动，
  副本就不是你以为的那棵树 ⇒ 脚本**先拒绝运行**（未跟踪文件 `??` 不算）。

其余纪律：文件级备份 + md5 逐字校验（禁用 git checkout --）；替换串不得包含 needle
作为子串；needle 必须唯一；★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）。

★★ 本脚本与 §143.38 的差别（**这一条本身就是本轮的门把判据推翻自己的产物**）：
  · §143.38 的门只管**仓内**（Go 源码 ↔ 迁移文件）；
  · §143.39 的门还管**跨语言**（shell 脚本 ↔ Go 判据 ↔ 迁移文件 ↔ runbook）
  ⇒ ⇒ **变异必须能打到「脚本 / 迁移 / 文档」这些非 Go 产物上**
  ⇒ ★ **只变异 Go 文件的门，会漏掉本轮真正的失守面**。

用法：
    python3 scripts/mutate-14339-retirement-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

GATE_REL = "admin/section_14339_retirement_precheck_test.go"
PRECHECK_REL = "scripts/audit/verify_890_cutover_precheck_211.sh"
MIG885_REL = "sql/migrations/startup/885_session_turns_host_tags.sql"
RUNBOOK_REL = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

MUTATIONS = [
    # ★★ M1 是本轮**最值钱**的一条：它打的是「两把尺对拍」那一条门。
    #   变异：把脚本里的 v1 臂 pattern **收窄**成只认 hot 表
    #   （`FROM _request_logs_hot`）⇒ 母表那条臂**数不到**
    #   ⇒ 臂数 2 → 1 ⇒ 判词从「旧二进制」降级成「介于两代之间」
    #   ⇒ ★★ **只跑语义自测的版本会绿**（Go 判据没动）
    #   ⇒ ⇒ **只有「shell 尺 vs Go 尺」对拍才能抓住它**。
    ("M1", "预检脚本的 v1 臂判据被收窄（漏掉母表臂 ⇒ 判词被悄悄降级）",
     PRECHECK_REL,
     "grep -cE 'FROM (public\\.)?_request_logs(_hot)?[[:space:]]'",
     "grep -cE 'FROM _request_logs_hot[[:space:]]'",
     "TestSection14339ShellPatternMatchesGoMatcher"),

    # M2：判词被改（阻塞项被写成建议）⇒ 判据 needle 缺失 ⇒ 红
    ("M2", "预检脚本的 BLOCKER-2 判词被改成建议（阻塞消失）",
     PRECHECK_REL,
     "BLOCKER-2 缺 host/tags 列 ⇒ 部署新二进制会 42703 断写（且 boot 不会失败）",
     "提示：部署前请自行确认 host/tags 列已就位",
     "TestSection14339Precheck211ScriptExists"),

    # M3：脚本的 here-doc SQL 里混进真 DDL ⇒ 只读性尺红
    ("M3", "预检脚本的 here-doc SQL 里混进真 DDL（⇒ 它不再是只读）",
     PRECHECK_REL,
     "SELECT current_database() AS db, current_user AS usr, pg_is_in_recovery() AS in_recovery;",
     "DROP TABLE IF EXISTS public._request_logs;",
     "TestSection14339Precheck211ScriptExists"),

    # M4：脚本又依赖上 pg_stat_statements（211 上不可用的量具）⇒ 依赖尺红
    ("M4", "预检脚本依赖 pg_stat_statements（211 上没进 preload ⇒ 量具不可用）",
     PRECHECK_REL,
     "SELECT count(*) AS ledger_rows, max(version) AS ledger_max FROM schema_migrations;",
     "SELECT count(*) FROM pg_stat_statements;",
     "TestSection14339Precheck211DoesNotDependOnPgStatStatements"),

    # M5：★ 迁移 885 的**两侧同形**被破坏（只给父表加列）
    #   ⇒ 违反 707 的 promote 列集合契约（热表排不进月分区）
    #   ⇒ 缺口清单门必须红
    ("M5", "迁移 885 的父表/hot 表列集合不同形（违反 707 的 promote 契约）",
     MIG885_REL,
     "ALTER TABLE public.session_turns_hot\n"
     "    ADD COLUMN IF NOT EXISTS host TEXT,\n"
     "    ADD COLUMN IF NOT EXISTS tags TEXT[] DEFAULT '{}'::TEXT[];",
     "ALTER TABLE public.session_turns_hot\n"
     "    ADD COLUMN IF NOT EXISTS host TEXT;",
     "TestSection14339GapListMatchesMigrationFiles"),

    # M6：runbook 里的核验手段名被抹掉 ⇒ 本节在案证据被改掉 ⇒ 红
    ("M6", "runbook 里的 211 预检脚本名被抹掉（本节在案证据被改掉）",
     RUNBOOK_REL,
     "verify_890_cutover_precheck_211",
     "verify_890_cutover_precheck_XXX",
     "TestSection14339RunbookHasSection"),

    # M7：runbook 里的**已部署二进制版本串**被改 ⇒ 证据失真 ⇒ 红
    ("M7", "runbook 里的已部署二进制版本串被改（部署漂移证据失真）",
     RUNBOOK_REL,
     "2.5.8-7a60b1b1-dirty-20261020-211",
     "2.5.8-7a60b1b2-dirty-20261020-211",
     "TestSection14339RunbookHasSection"),

    # M8：★ Go 判据被**改宽**（canonical 视图也算 v1 臂）⇒ 语义自测红
    # ★ 第一版 M8 作废：替换串是「在 needle 后面追加一段」⇒ **替换串包含 needle
    #   作为子串** ⇒ 纪律拦下 ⇒ **作废的轮次不算「门没牙」**。
    # ⇒ 改成「**换掉整行**」：把模式本身写成会把 canonical 视图也匹配上的形态。
    ("M8", "Go 判据被改宽（canonical 视图被误判成 v1 臂）",
     GATE_REL,
     "var v1ArmPattern14339 = regexp.MustCompile(`FROM (public\\.)?_request_logs(_hot)?[\\s]`)",
     "var v1ArmPattern14339 = regexp.MustCompile(`FROM (public\\.)?_request_logs(_hot)?[\\s]|request_logs_with_current_month`)",
     "TestSection14339V1ArmMatcherSemantics"),
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

    work = tempfile.mkdtemp(prefix="p14339mut-")
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
