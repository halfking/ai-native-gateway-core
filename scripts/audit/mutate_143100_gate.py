#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.100 L3 回退 turns 腿索引缺口门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_143100_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 纪律（★ 与 §143.96 ~ §143.99 同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ④ 环境指纹对拍；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ 每个变异自带【效果探针】（方向 + 出现次数）；
★   ⑦ 跑变异台前必须先 commit 或 stash。

★ ★★ 本台特有的两条：
★   ⑪ ★★★★ **M05 是「必须红」臂**：★ 它把 L3 回退的顶层排序键从
★      `ORDER BY rl.ts DESC` 改成 `ORDER BY rl.turn_no DESC`
★      （★ 实测这个改法快 4,762×，★ 是最容易被下一个人顺手改掉的误改）
★      ⇒ ⇒ **门必须红** ⇒ ⇒ 因为它【不是等价优化】（秩不一致率 93.9%）。
★   ⑫ ★★★★ **M08 是「期望绿」臂**：★ 它在 `sql/migrations/startup/` 下造一条
★      turns 族 `(session_id, ts DESC)` 的迁移（★ 模拟属主授权 ㉔ 后的落地）
★      ⇒ ⇒ **门必须【仍绿】** ⇒ ⇒ 因为「缺陷已登记」与「缺陷已修好」两种状态都应当是绿的
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

GATE = os.path.join(ROOT, "cmd/gateway/section_143100_l3_turns_leg_index_test.go")
AUDIT = os.path.join(ROOT, "scripts/audit/audit_l3_turns_leg_index_gap.sql")
WIRING = os.path.join(ROOT, "cmd/gateway/main_v3_wiring.go")
MIGDIR = os.path.join(ROOT, "sql/migrations/startup")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")

# ★ M08 造的临时迁移（★ 必须删掉，★ 否则它会进 git status）
TMP_MIG = os.path.join(MIGDIR, "zzz_mutate_143100_tmp_session_turns_session_ts_idx.sql")

WATCHED = (GATE, AUDIT, WIRING, RUNBOOK, LESSONS, AGENTS)

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
        ["go", "test", "./cmd/gateway/", "-run", "TestSection143100", "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env)
    return p.returncode == 0, (p.stdout + p.stderr)


RESULTS = []


def arm(name, expect_green, mutate, probe=None):
    for path in WATCHED:
        snap(path)
    before_status = git_status()
    before_md5 = {p: file_md5(p) for p in WATCHED}

    try:
        detail = mutate()
        changed = [p for p in WATCHED if file_md5(p) != before_md5[p]]
        if not changed and not os.path.exists(TMP_MIG):
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
        # ★ ★ M08 的临时迁移必须【真的删掉】（★ 它是 untracked 文件 ⇒ 不在 backups 里）
        # ★ ★ 只清空不行 —— ★ 空文件仍在 ⇒ ⇒ git status 会多一行 ⇒ ⇒ 环境指纹对拍必失败。
        if os.path.exists(TMP_MIG):
            try:
                os.remove(TMP_MIG)
            except OSError as exc:
                print("❌ 临时迁移删除失败：%s（%s）" % (TMP_MIG, exc))
                sys.exit(1)

    after_status = git_status()
    if after_status != before_status:
        RESULTS.append((name, False, "★ 环境指纹不一致（工作树状态变了）"))
        sys.exit(1)


def replace_in_files(paths, old, new, expect_count=None):
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
    def _p():
        if needle in read(path):
            return False, "needle `%s` 仍然存在" % needle
        return True, ""
    return _p


def probe_needle_present(path, needle):
    def _p():
        if needle not in read(path):
            return False, "needle `%s` 没有出现（★ 变异没生效？）" % needle
        return True, ""
    return _p


def probe_count_changed(path, needle, expect_delta):
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

def m01_drop_rank_equiv_proof():
    """★ 删掉审计脚本的 Q5（秩一致率反证）⇒ 门应红（判据 ①）。"""
    s = read(AUDIT)
    i = s.find("=== Q5")
    j = s.find("=== Q6")
    if i < 0 or j < 0:
        raise AssertionError("★ 审计脚本里找不到 Q5 / Q6")
    write(AUDIT, s[:i] + "-- ★ （变异：Q5 秩一致率反证被删）\n" + s[j:])
    return "（删掉了 Q5 段）"


def m02_audit_script_gets_write():
    """★ 让审计脚本含行首写关键字 ⇒ 它不再是只读 ⇒ 门应红（判据 ①）。"""
    s = read(AUDIT)
    write(AUDIT, s + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS zz_mut ON public.session_turns (session_id, ts DESC);\n")
    return "（追加了一行 CREATE INDEX）"


def m03_drop_runbook_marker():
    """★ 删掉 runbook 里的 §143.100 标记 ⇒ 缺陷登记消失 ⇒ 门应红（判据 ②/④）。"""
    n = replace_in_files([RUNBOOK], "§143.100", "§143.10X")
    return "（改了 %d 处）" % n


def m04_drop_key_number():
    """★ 删掉 runbook 里的关键数字 4,762 ⇒ 登记失去可复算性 ⇒ 门应红（判据 ②）。"""
    n = replace_in_files([RUNBOOK], "4,762", "4762")
    return "（改了 %d 处）" % n


def m05_swap_sort_key():
    """★★ 换排序键（ts DESC → turn_no DESC）★ 快 4,762× 但不是等价优化 ⇒ 门应红（判据 ③）。"""
    n = replace_in_files([WIRING], "ORDER BY rl.ts DESC", "ORDER BY rl.turn_no DESC")
    if n < 1:
        raise AssertionError("★ 没找到 `ORDER BY rl.ts DESC`")
    return "（改了 %d 处）" % n


def m06_drop_lessons_marker():
    """★ 删掉 LESSONS 里的 §296 ⇒ 通用式没沉淀 ⇒ 门应红（判据 ④）。"""
    n = replace_in_files([LESSONS], "§296", "§999")
    return "（改了 %d 处）" % n


def m07_drop_agents_marker():
    """★ 删掉 AGENTS 里的 R145 ⇒ 交接没更新 ⇒ 门应红（判据 ④）。"""
    n = replace_in_files([AGENTS], "R145", "R999")
    return "（改了 %d 处）" % n


def m08_add_session_ts_index_migration():
    """★ ★ 模拟属主授权 ㉔ 后的落地：加一条 turns 族 (session_id, ts DESC) 迁移 ⇒ 门应【仍绿】。"""
    body = (
        "-- 变异台临时文件（§143.100 M08）：模拟 turns 族 (session_id, ts DESC) 索引已落地。\n"
        "CREATE INDEX CONCURRENTLY IF NOT EXISTS session_turns_session_ts_idx\n"
        "  ON ONLY public.session_turns USING btree (session_id, ts DESC);\n"
    )
    write(TMP_MIG, body)
    return "（造了一条临时迁移：%s）" % os.path.basename(TMP_MIG)


def main():
    print("=" * 78)
    print("§143.100 变异台：验证「门绿 ≠ 门有牙」")
    print("=" * 78)

    arm("M01 删掉审计脚本的 Q5 秩一致率反证", False, m01_drop_rank_equiv_proof,
        probe_needle_gone(AUDIT, "rank_mismatch"))
    arm("M02 审计脚本混入 CREATE INDEX（不再只读）", False, m02_audit_script_gets_write,
        probe_needle_present(AUDIT, "CREATE INDEX CONCURRENTLY IF NOT EXISTS zz_mut"))
    arm("M03 删掉 runbook 的 §143.100 标记", False, m03_drop_runbook_marker,
        probe_needle_gone(RUNBOOK, "§143.100"))
    arm("M04 删掉 runbook 的关键数字 4,762", False, m04_drop_key_number,
        probe_needle_gone(RUNBOOK, "4,762"))
    arm("M05 换排序键 ts DESC -> turn_no DESC（★ 快 4,762× 但语义变了）", False, m05_swap_sort_key,
        probe_needle_present(WIRING, "ORDER BY rl.turn_no DESC"))
    arm("M06 删掉 LESSONS 的 §296 标记", False, m06_drop_lessons_marker,
        probe_needle_gone(LESSONS, "§296"))
    arm("M07 删掉 AGENTS 的 R145 标记", False, m07_drop_agents_marker,
        probe_needle_gone(AGENTS, "R145"))
    arm("M08 加一条 (session_id, ts DESC) 迁移（合法变更，期望仍绿）", True,
        m08_add_session_ts_index_migration,
        probe_needle_present(TMP_MIG, "session_id, ts DESC"))

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
