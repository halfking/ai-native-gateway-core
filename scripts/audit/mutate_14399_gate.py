#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.99 会话正文族读侧「长尾放大」门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14399_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 纪律（★ 与 §143.96 / §143.97 / §143.98 同构，已在那里逐条落地）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 每个变异都必须自带【效果探针】 —— ★ 「目标 needle 在【判据实际读的
★      那一面】里的出现次数变成 0 / 出现」+「文件内容确实变了」；
★   ⑦ ★★★ 探针的【方向】必须与变异的方向一致；
★   ⑧ ★★★ **探针的【取值来源】也要与判据对齐**；
★   ⑨ ★★★ 跑变异台前必须先 commit 或 stash —— ★ 否则读数不可信；
★   ⑩ ★★★ **「门没牙」要先过【惰性变异】筛查** —— ★ 用判据自己的谓词复算一次。

★ ★★ 本台特有的一条：
★   ⑪ ★★★★ **M04 是「期望绿」臂**：★ 它给 `SessionFamilyTurnsForSessionSQL` 的 turns 腿
★      加上 `LIMIT`（★ 这正是属主授权 ㉓ 之后会做的那个变更）⇒ ⇒ **门必须【仍绿】**
★      ⇒ ⇒ 因为「缺陷已登记」与「缺陷已修好」两种状态都应当是绿的
★      ⇒ ⇒ ★ **⇒ 一道门若在缺陷被修好之后变红，那它就是在阻止修复 ⇒ 必须改**
"""
import hashlib
import os
import subprocess
import sys

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

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GATE = os.path.join(ROOT, "cmd/gateway/section_14399_read_amplification_test.go")
AUDIT = os.path.join(ROOT, "scripts/audit/audit_session_bodies_read_amplification.sql")
SCHEMA_GO = os.path.join(ROOT, "db/request_logs_view_schema.go")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")

WATCHED = (GATE, AUDIT, SCHEMA_GO, RUNBOOK, LESSONS, AGENTS)

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
        ["go", "test", "./cmd/gateway/", "-run", "TestSection14399", "-count=1"],
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


def replace_in_func(path, func_sig, old, new, expect_count=1):
    """★ 只在【某个函数的函数体】里替换 —— ★ 因为判据 ③ 读的就是那个函数体。

    ★ ★ 为什么必须限定作用域：★ `UNION ALL SELECT` 在整个文件里出现 3 处
    ★   ⇒ ⇒ 全局替换会改到【判据读不到的地方】⇒ ⇒ 那是「变异惰性」
    ★   ⇒ ⇒ 判据仍然会绿 ⇒ ⇒ 变异臂就成了假阳性。
    """
    s = read(path)
    i = s.find(func_sig)
    if i < 0:
        raise AssertionError("★ 在 %s 里找不到 `%s`" % (path, func_sig))
    j = s.find("\n}\n", i)
    if j < 0:
        raise AssertionError("★ 找不到 %s 的函数体结尾" % func_sig)
    body = s[i:j]
    n = body.count(old)
    if n != expect_count:
        raise AssertionError("★ 函数体内期望 %s 出现 %d 次，实际 %d 次" % (old, expect_count, n))
    write(path, s[:i] + body.replace(old, new) + s[j:])
    return n


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


def probe_count_changed(path, needle, expect_delta):
    """★ 探针（方向：出现次数必须【变化恰好 expect_delta】）。"""
    before = backups[path].decode("utf-8").count(needle)
    def _p():
        now = read(path).count(needle)
        if now - before != expect_delta:
            return False, "needle `%s` 出现次数从 %d 变成 %d（期望变化 %+d）" % (
                needle, before, now, expect_delta)
        return True, ""
    return _p


# ═══════════════════════════════════════════════════════════════════════
# 变异臂
# ═══════════════════════════════════════════════════════════════════════

def m01_drop_anchor_selfcheck():
    """★ 删掉审计脚本的 Q2 量具自检锚点 ⇒ 门应红（判据 ①）。"""
    s = read(AUDIT)
    i = s.find("=== Q2")
    if i < 0:
        raise AssertionError("★ 审计脚本里找不到 Q2")
    j = s.find("=== Q3")
    if j < 0:
        raise AssertionError("★ 审计脚本里找不到 Q3")
    write(AUDIT, s[:i] + "-- ★ （变异：Q2 量具自检锚点被删）\n" + s[j:])
    return "（删掉了 Q2 段）"


def m02_audit_script_gets_write():
    """★ 让审计脚本含行首写关键字 ⇒ 它不再是只读 ⇒ 门应红（判据 ①）。"""
    s = read(AUDIT)
    write(AUDIT, s + "\nDELETE FROM public.session_bodies_hot WHERE false;\n")
    return "（追加了一行 DELETE）"


def m03_drop_tail_ratio_selfcheck():
    """★ 删掉 Q8 的长尾算术自检（tail_ratio）⇒ 门应红（判据 ①）。"""
    s = read(AUDIT)
    i = s.find("=== Q8")
    if i < 0:
        raise AssertionError("★ 审计脚本里找不到 Q8")
    j = s.find("=== Q9")
    if j < 0:
        raise AssertionError("★ 审计脚本里找不到 Q9")
    write(AUDIT, s[:i] + "-- ★ （变异：Q8 长尾算术自检被删）\n" + s[j:])
    return "（删掉了 Q8 段）"


def m04_add_turns_leg_limit():
    """★ ★ 给 turns 腿加上轮次上限（★ 属主授权 ㉓ 之后会做的那个变更）⇒ 门应【仍绿】。"""
    # ★★★ UNION 的分支里带 LIMIT 必须【加括号】⇒ ⇒ 三处一起改：
    #   ① 第一条腿的 WHERE 收尾是 `... $1" +`（★ 无右括号）
    #   ② `UNION ALL SELECT` ⇒ `UNION ALL (SELECT`
    #   ③ 第二条腿的 WHERE 收尾是 `... $1)"`（★ 有右括号）
    sig = "func SessionFamilyTurnsForSessionSQL() string {"
    n1 = replace_in_func(SCHEMA_GO, sig,
                         '" WHERE t.session_id = $1" +',
                         '" WHERE t.session_id = $1 LIMIT 50)" +')
    n2 = replace_in_func(SCHEMA_GO, sig,
                         '" UNION ALL SELECT "',
                         '" UNION ALL (SELECT "')
    n3 = replace_in_func(SCHEMA_GO, sig,
                         '" WHERE t.session_id = $1)"',
                         '" WHERE t.session_id = $1 LIMIT 50)"')
    return "（改了 %d 处：turns 腿两条各加 LIMIT 50 并给分支加括号）" % (n1 + n2 + n3)


def m05_drop_runbook_marker():
    """★ 删掉 runbook 里的 §143.99 标记 ⇒ 缺陷登记消失 ⇒ 门应红（判据 ③）。"""
    n = replace_in_files([RUNBOOK], "§143.99", "§143.9X")
    return "（改了 %d 处）" % n


def m06_break_tail_ratio():
    """★ 把门里的 max 常量改小 ⇒ tail_ratio 掉到 10 以下 ⇒ 门应红（判据 ②）。"""
    n = replace_in_files([GATE],
                         "section14399MeasuredMaxMs   = 10_200",
                         "section14399MeasuredMaxMs   = 1_200")
    return "（改了 %d 处）" % n


def m07_break_identity():
    """★ 把门里的 mean 常量改大 ⇒ calls × mean ≠ total ⇒ 门应红（判据 ② 恒等式）。"""
    n = replace_in_files([GATE],
                         "section14399MeasuredMeanMs  = 182.4",
                         "section14399MeasuredMeanMs  = 18240")
    return "（改了 %d 处）" % n


def m08_drop_agents_marker():
    """★ 删掉 AGENTS 里的 R144 标记 ⇒ 门应红（判据 ④）。"""
    n = replace_in_files([AGENTS], "R144", "R999")
    return "（改了 %d 处）" % n


def main():
    print("=" * 78)
    print("§143.99 变异台：验证「门绿 ≠ 门有牙」")
    print("=" * 78)

    arm("M01 删掉审计脚本的 Q2 量具自检锚点", False, m01_drop_anchor_selfcheck,
        probe_needle_gone(AUDIT, "anchor_zero_rows"))
    arm("M02 审计脚本混入写语句（不再只读）", False, m02_audit_script_gets_write,
        probe_needle_present(AUDIT, "DELETE FROM public.session_bodies_hot"))
    arm("M03 删掉审计脚本的 Q8 长尾算术自检", False, m03_drop_tail_ratio_selfcheck,
        probe_needle_gone(AUDIT, "tail_ratio"))
    arm("M04 turns 腿加上轮次上限（合法变更，期望仍绿）", True, m04_add_turns_leg_limit,
        probe_count_changed(SCHEMA_GO, "LIMIT 50", 2))
    arm("M05 删掉 runbook 的 §143.99 标记", False, m05_drop_runbook_marker,
        probe_needle_gone(RUNBOOK, "§143.99"))
    arm("M06 把 max 常量改小（tail_ratio 掉出阈值）", False, m06_break_tail_ratio,
        probe_needle_present(GATE, "section14399MeasuredMaxMs   = 1_200"))
    arm("M07 把 mean 常量改大（恒等式崩）", False, m07_break_identity,
        probe_needle_present(GATE, "section14399MeasuredMeanMs  = 18240"))
    arm("M08 删掉 AGENTS 的 R144 标记", False, m08_drop_agents_marker,
        probe_needle_gone(AGENTS, "R144"))

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
