#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.98 会话正文族 promote 预算不变式门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14398_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 纪律（★ 与 §143.96 / §143.97 同构，已在那里逐条落地）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 每个变异都必须自带【效果探针】 —— ★ 「目标 needle 在【判据实际读的
★      那一面】里的出现次数变成 0 / 出现」+「文件内容确实变了」；
★   ⑦ ★★★ 探针的【方向】必须与变异的方向一致；
★   ⑧ ★★★ **探针的【取值来源】也要与判据对齐** —— ★ 不只是「作用域」，
★      还有「取哪个值」（★ §143.97 第 5 处自身问题）；
★   ⑨ ★★★ 跑变异台前必须先 commit 或 stash —— ★ 否则读数不可信；
★  ⑩ ★★★ **「门没牙」要先过【惰性变异】筛查** —— ★ 用判据自己的谓词复算一次。

★ ★★ 本台特有的一条：
★   ⑪ ★★ **M04 是「期望绿」臂**：★ 它把代码默认批大小从 5,000 改成 1,000
★      （★ 这正是属主授权后会做的那个变更）⇒ ⇒ **门必须【仍绿】**
★      ⇒ ⇒ 因为「缺陷已登记」与「默认值已合规」两种状态都应当是绿的
★      ⇒ ⇒ ★ **⇒ 一道门若在缺陷被修好之后变红，那它就是在阻止修复 ⇒ 必须改**
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GATE = os.path.join(ROOT, "cmd/gateway/section_14398_promote_budget_test.go")
AUDIT = os.path.join(ROOT, "scripts/audit/audit_session_bodies_promote_budget.sql")
PARTM = os.path.join(ROOT, "bg/partition_manager.go")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")

WATCHED = (GATE, AUDIT, PARTM, RUNBOOK, LESSONS, AGENTS)

backups = {}


def snap(path):
    if path not in backups:
        with open(path, "rb") as f:
            backups[path] = f.read()


def restore_all():
    for path, data in backups.items():
        with open(path, "wb") as f:
            f.write(data)
    bad = []
    for path, data in backups.items():
        with open(path, "rb") as f:
            now = f.read()
        if hashlib.md5(now).hexdigest() != hashlib.md5(data).hexdigest():
            bad.append(path)
    if bad:
        print("❌ 还原校验失败：", bad)
        sys.exit(1)
    print("✅ 还原校验：%d 个文件 md5 逐字一致" % len(backups))


def git_status():
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                       capture_output=True, text=True)
    return p.stdout


def file_md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def read(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def run_gate():
    env = dict(os.environ)
    env.pop("TEST_DATABASE_URL", None)
    p = subprocess.run(
        ["go", "test", "./cmd/gateway/", "-run", "TestSection14398", "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env)
    return p.returncode == 0, (p.stdout + p.stderr)


RESULTS = []


def arm(name, expect_green, mutate, probe=None):
    """跑一条变异臂。

    expect_green=True  ⇒ 变异合法 ⇒ 门应当**仍绿**。
    expect_green=False ⇒ 变异应当让门**变红**（证明门有牙）。
    """
    for path in WATCHED:
        snap(path)
    before_status = git_status()
    before_md5 = {p: file_md5(p) for p in WATCHED}

    try:
        detail = mutate()
        changed = [p for p in WATCHED if file_md5(p) != before_md5[p]]
        if not changed:
            RESULTS.append((name, False, "★ 变异没生效（没有任何受监控文件的字节变化）"))
            return

        if probe is not None:
            ok, msg = probe()
            if not ok:
                RESULTS.append((name, False, "★ 效果探针失败：%s" % msg))
                return

        green, out = run_gate()
        if expect_green and not green:
            RESULTS.append((name, False, "★ 期望绿但门红了（变异不合法 / 门过严）\n%s" % out[-1200:]))
        elif (not expect_green) and green:
            RESULTS.append((name, False, "★ 期望红但门仍绿 ⇒ ★ 门没牙"))
        else:
            RESULTS.append((name, True, "OK（改动 %d 个文件）%s" % (len(changed), detail)))
    finally:
        restore_all()

    after_status = git_status()
    if after_status != before_status:
        RESULTS.append((name, False, "★ 环境指纹不一致（工作树状态变了）"))
        sys.exit(1)


def replace_in_files(paths, old, new, expect_count=None):
    """在若干文件里替换全部出现处；★ 返回 (改了几处, 实际出现次数)。"""
    total = 0
    for p in paths:
        s = read(p)
        n = s.count(old)
        if n:
            write(p, s.replace(old, new))
        total += n
    if expect_count is not None and total != expect_count:
        raise AssertionError("★ 期望改 %d 处，实际 %d 处" % (expect_count, total))
    return total


def probe_needle_gone(path, needle):
    """★ 探针（方向：needle 必须【归零】）—— 用于「删掉某物」的变异。"""
    def _p():
        if needle in read(path):
            return False, "needle `%s` 仍然存在" % needle
        return True, ""
    return _p


def probe_needle_present(path, needle):
    """★ 探针（方向：needle 必须【出现】）—— 用于「引入某物」的变异。"""
    def _p():
        if needle not in read(path):
            return False, "needle `%s` 没有出现（★ 变异没生效？）" % needle
        return True, ""
    return _p


def probe_body_changed(path):
    before = backups[path]
    def _p():
        with open(path, "rb") as f:
            now = f.read()
        if now == before:
            return False, "文件没变"
        return True, ""
    return _p


# ═══════════════════════════════════════════════════════════════════════
# 变异臂
# ═══════════════════════════════════════════════════════════════════════

def m01_recommended_batch_to_5000():
    """★ 把推荐批大小改成 5,000 ⇒ 它本身就超预算 ⇒ 门应红。"""
    n = replace_in_files([GATE],
                         "section14398RecommendedBatch = 1000",
                         "section14398RecommendedBatch = 5000")
    return "（改了 %d 处）" % n


def m02_drop_q9_selfcheck():
    """★ 删掉审计脚本的 Q9 预算自检 ⇒ 门应红。"""
    s = read(AUDIT)
    i = s.find("=== Q9")
    if i < 0:
        raise AssertionError("★ 审计脚本里找不到 Q9")
    write(AUDIT, s[:i] + "-- ★ （变异：Q9 自检被删）\n")
    return "（删掉了 Q9 段）"


def m03_audit_script_gets_write():
    """★ 让审计脚本含行首写关键字 ⇒ 它不再是只读 ⇒ 门应红。"""
    s = read(AUDIT)
    write(AUDIT, s + "\nDELETE FROM public.session_bodies_hot WHERE false;\n")
    return "（追加了一行 DELETE）"


def m04_fix_code_default():
    """★ 把代码默认批大小改成 1,000（★ 属主授权后会做的那个变更）⇒ 门应【仍绿】。"""
    n = replace_in_files([PARTM],
                         "const promoteBatchSize = 5000",
                         "const promoteBatchSize = 1000")
    return "（改了 %d 处）" % n


def m05_drop_runbook_marker():
    """★ 删掉 runbook 里的 §143.98 标记 ⇒ 缺陷登记消失 ⇒ 门应红。"""
    n = replace_in_files([RUNBOOK], "§143.98", "§143.99")
    return "（改了 %d 处）" % n


def m06_change_batch_loop_condition():
    """★ 把批次循环条件变形（★ 有人在修「保底 1 批」那个死代码）⇒ 门应红。"""
    n = replace_in_files([PARTM],
                         "if cycleCtx.Err() != nil || (batches >= promoteCycleMaxBatches && tableRanBatch) {",
                         "if batches >= promoteCycleMaxBatches && tableRanBatch {")
    return "（改了 %d 处）" % n


def m07_drop_agents_marker():
    """★ 删掉 AGENTS 里的 R143 标记 ⇒ 门应红。"""
    n = replace_in_files([AGENTS], "R143", "R999")
    return "（改了 %d 处）" % n


def main():
    print("=" * 78)
    print("§143.98 变异台：验证「门绿 ≠ 门有牙」")
    print("=" * 78)

    arm("M01 推荐批大小改成 5,000（超预算）", False, m01_recommended_batch_to_5000,
        probe_needle_present(GATE, "section14398RecommendedBatch = 5000"))
    arm("M02 删掉审计脚本的 Q9 自检", False, m02_drop_q9_selfcheck,
        probe_needle_gone(AUDIT, "will_time_out"))
    arm("M03 审计脚本混入写语句（不再只读）", False, m03_audit_script_gets_write,
        probe_needle_present(AUDIT, "DELETE FROM public.session_bodies_hot"))
    arm("M04 代码默认改成 1,000（合法变更，期望仍绿）", True, m04_fix_code_default,
        probe_needle_present(PARTM, "const promoteBatchSize = 1000"))
    arm("M05 删掉 runbook 的 §143.98 标记", False, m05_drop_runbook_marker,
        probe_needle_gone(RUNBOOK, "§143.98"))
    arm("M06 批次循环条件变形（有人在修那个死代码）", False, m06_change_batch_loop_condition,
        probe_needle_gone(PARTM, "if cycleCtx.Err() != nil || (batches >= promoteCycleMaxBatches"))
    arm("M07 删掉 AGENTS 的 R143 标记", False, m07_drop_agents_marker,
        probe_needle_gone(AGENTS, "R143"))

    print("-" * 78)
    ok = 0
    for name, passed, msg in RESULTS:
        mark = "✅" if passed else "❌"
        print("%s %-52s %s" % (mark, name, msg if not passed else ""))
        if passed:
            ok += 1
    print("-" * 78)
    print("结果：%d/%d 通过" % (ok, len(RESULTS)))
    print("环境指纹：")
    st = git_status()
    print("  git status 行数 =", len([l for l in st.split("\n") if l.strip()]))
    return 0 if ok == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
