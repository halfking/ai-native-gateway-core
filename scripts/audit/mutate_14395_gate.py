#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.95 session_turns 留存归属门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14395_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 六条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M16 / M17）；
★   ④ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑥ 变异必须落在【判据实际读到的那一处】。

★ ★★ 本轮特有的两条：
★   ⑦ ★★ 变异落在【仓内源码/迁移】上时（★ bg/partition_manager.go、
★      ★ sql/migrations/startup/*）⇒ ⇒ 必须 `finally` 兜底还原 + md5 校验；
★   ⑧ ★★ 本轮门**刻意不钉活数字**（★ hot 的行数/体积每小时都在涨）⇒ ⇒
★      ⇒ 变异台也**不许**拿活数字当判据 ⇒ ⇒ ⇒ 只钉稳定标识。
"""
import hashlib
import os
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
SCRIPT = os.path.join(ROOT, "scripts/audit/verify_session_turns_retention_ownership.sql")
PARTITION = os.path.join(ROOT, "bg/partition_manager.go")
PROMOTE_BASE = os.path.join(ROOT, "sql/migrations/startup/869_promote_session_turns_trace_events.sql")
SELF_HEAL = os.path.join(ROOT, "sql/migrations/startup/695_request_logs_promote_final_success_self_heal.sql")
V1_RETIRE = os.path.join(ROOT, "sql/migrations/startup/890_drop_request_logs_v1_family.sql")
UNIQUE_IDX = os.path.join(ROOT, "sql/migrations/startup/707_session_turns_s1a.sql")

WATCHED = (RUNBOOK, LESSONS, AGENTS, SCRIPT, PARTITION,
           PROMOTE_BASE, SELF_HEAL, V1_RETIRE, UNIQUE_IDX)

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


def env_fingerprint():
    return {"status": git_status(), "md5": {p: file_md5(p) for p in WATCHED}}


ENV_BEFORE = env_fingerprint()


def run_gate(pattern):
    env = dict(os.environ)
    env.pop("TEST_DATABASE_URL", None)
    p = subprocess.run(
        ["go", "test", "./cmd/gateway/", "-run", pattern, "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env, timeout=900,
    )
    return p.returncode != 0, (p.stdout + p.stderr)


results = []


def check(name, expect_red, pattern, mutate, restore):
    for p in WATCHED:
        snap(p)
    try:
        mutate()
        red, out = run_gate(pattern)
        ok = (red == expect_red)
        if expect_red:
            print(("🔴 " if red else "🟢 ") + name + (" ⇒ 变红 ✔" if red else " ⇒ 仍然绿 ❌"))
        else:
            print(("🟢 " if not red else "🔴 ") + name + (" ⇒ 仍然绿 ✔" if not red else " ⇒ 变红 ❌"))
        if not ok:
            print(out[-1800:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, s):
    with open(path, "w", encoding="utf-8") as f:
        f.write(s)


def replace_all(path, old, new, expect_n=None):
    """★ 变异必须改【全部】出现处（★ 并声明改了几处）。"""
    s = read(path)
    n = s.count(old)
    assert n > 0, "needle 在 %s 里出现 0 次：%r" % (path, old)
    if expect_n is not None:
        assert n == expect_n, "needle 在 %s 里出现 %d 次（期望 %d）" % (path, n, expect_n)
    write(path, s.replace(old, new))
    return n


def replace_in_segment(path, header, old, new, expect_n=None):
    """★ 只在某个 `### ` 段内替换（★ 避免误伤其它段）。"""
    s = read(path)
    i = s.index(header)
    j = s.find("\n### ", i + 1)
    if j < 0:
        j = len(s)
    seg = s[i:j]
    n = seg.count(old)
    assert n > 0, "needle 在 %s 的 %s 段里出现 0 次：%r" % (path, header, old)
    if expect_n is not None:
        assert n == expect_n, "needle 在 %s 的 %s 段里出现 %d 次（期望 %d）" % (path, header, n, expect_n)
    write(path, s[:i] + seg.replace(old, new) + s[j:])
    return n


def restore_path(path):
    with open(path, "wb") as f:
        f.write(backups[path])


# ---------- M1：★ promote 自己的去重键探针被摘掉（★ 判据①） ----------
def m1_mut():
    replace_all(SCRIPT,
                "a.request_id = s.request_id AND a.tenant_id = s.tenant_id",
                "a.request_id = s.request_id AND a.tenant_id = s.tenant_id_x",
                expect_n=1)
def m1_restore():
    restore_path(SCRIPT)


# ---------- M2：★ 父表探针的 is_final_success 被摘掉（★ 判据①） ----------
def m2_mut():
    replace_all(SCRIPT,
                "AND p.is_final_success IS TRUE",
                "AND p.is_final_success_x IS TRUE",
                expect_n=1)
def m2_restore():
    restore_path(SCRIPT)


# ---------- M3：★★ 父表探针的键被换成 promote 自己的键（★ 判据①：★ 本轮第一次犯的错） ----------
def m3_mut():
    replace_all(SCRIPT,
                "AND p.session_id = h.session_id",
                "AND p.request_id = h.request_id",
                expect_n=3)
    replace_all(SCRIPT,
                "AND p.partition_date = h.partition_date",
                "AND p.request_id = h.request_id",
                expect_n=3)
def m3_restore():
    restore_path(SCRIPT)


# ---------- M4：★ 用 `success` 冒充 `is_final_success`（★ 判据①的反向禁） ----------
def m4_mut():
    replace_all(SCRIPT,
                "AS hot_fs_rows",
                "AS hot_success",
                expect_n=1)
def m4_restore():
    restore_path(SCRIPT)


# ---------- M5：★ 8h 保留窗判据被改成 24h（★ 判据②） ----------
def m5_mut():
    replace_all(SCRIPT,
                "count(*) FILTER (WHERE ts <  now() - interval '8 hours') AS older_than_8h,",
                "count(*) FILTER (WHERE ts <  now() - interval '24 hours') AS older_than_8h,",
                expect_n=1)
def m5_restore():
    restore_path(SCRIPT)


# ---------- M6：★ Go 侧保留窗常量被改（★ 判据②：★ 仓内源码） ----------
def m6_mut():
    replace_all(PARTITION,
                "const DefaultRetentionWindow = 8 * time.Hour",
                "const DefaultRetentionWindow = 16 * time.Hour",
                expect_n=1)
def m6_restore():
    restore_path(PARTITION)


# ---------- M7：★ Go 侧批大小被改（★ 判据②：★ 仓内源码） ----------
def m7_mut():
    replace_all(PARTITION,
                "const promoteBatchSize = 5000",
                "const promoteBatchSize = 6000",
                expect_n=1)
def m7_restore():
    restore_path(PARTITION)


# ---------- M8：★ 869 基线的排水判据被改（★ 判据②：★ 仓内迁移） ----------
def m8_mut():
    replace_all(PROMOTE_BASE,
                "WHERE h.ts < statement_timestamp() - p_retention",
                "WHERE h.ts < statement_timestamp() - p_retention_x",
                expect_n=2)
def m8_restore():
    restore_path(PROMOTE_BASE)


# ---------- M9：★ 父表饿死的判据被摘掉（★ 判据③） ----------
def m9_mut():
    replace_all(SCRIPT,
                "n_tup_ins, n_tup_upd, n_tup_del, n_live_tup, idx_scan, seq_scan",
                "n_tup_upd, n_tup_del, n_live_tup, idx_scan, seq_scan",
                expect_n=1)
def m9_restore():
    restore_path(SCRIPT)


# ---------- M10：★ 修法样板的 demote 步被摘掉（★ 判据④：★ 仓内迁移） ----------
def m10_mut():
    replace_all(SELF_HEAL,
                "SET is_final_success = FALSE",
                "SET is_final_success_x = FALSE",
                expect_n=1)
def m10_restore():
    restore_path(SELF_HEAL)


# ---------- M11：★ v1 族退役的证据被摘掉（★ 判据④：★ 仓内迁移） ----------
def m11_mut():
    replace_all(V1_RETIRE, "request_logs", "request_logs_x", expect_n=66)
def m11_restore():
    restore_path(V1_RETIRE)


# ---------- M12：★ 23505 的来源索引名被改（★ 判据④：★ 仓内迁移） ----------
def m12_mut():
    replace_all(UNIQUE_IDX,
                "uq_session_turns_final_success",
                "uq_session_turns_final_success_x",
                expect_n=3)
def m12_restore():
    restore_path(UNIQUE_IDX)


# ---------- M13：★ 「剥注释后匹配」被摘掉（★ 判据⑤配套） ----------
def m13_mut():
    replace_all(SCRIPT,
                "regexp_replace(query, '--[^\\n]*', '', 'g')",
                "query",
                expect_n=1)
def m13_restore():
    restore_path(SCRIPT)


# ---------- M14：★ 可执行面里混进一条 DELETE（★ 判据⑤：只读守卫） ----------
def m14_mut():
    s = read(SCRIPT)
    write(SCRIPT, s.rstrip("\n") + "\nDELETE FROM public.session_turns_2026_09;\n")
def m14_restore():
    restore_path(SCRIPT)


# ---------- M15：★ §143.95 段的关键标识被改（★ 判据⑥） ----------
def m15_mut():
    replace_in_segment(RUNBOOK, "### §143.95",
                       "gw_omp-global-kxpms", "gw_omp-global-kxpms_x", expect_n=1)
def m15_restore():
    restore_path(RUNBOOK)


# ---------- M16：★ LESSONS §289 / AGENTS R139 编号被改（★ 判据⑥配套） ----------
def m16_mut():
    replace_all(LESSONS, "## §289", "## §290", expect_n=1)
    replace_all(AGENTS, "R139 轮", "R140 轮", expect_n=1)
def m16_restore():
    restore_path(LESSONS)
    restore_path(AGENTS)


# ---------- M17（★ 期望【仍然绿】）：★ 写语句只出现在【注释】里 ----------
def m17_mut():
    s = read(SCRIPT)
    anchor = "SET statement_timeout = '600s';\n"
    assert s.count(anchor) == 1
    ins = "-- DELETE FROM public.session_turns_2026_09;  ★ 变异体：写语句只出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m17_restore():
    restore_path(SCRIPT)


# ---------- M18（★ 期望【仍然绿】）：★ 反向禁的关键词只出现在【注释】里 ----------
def m18_mut():
    s = read(SCRIPT)
    anchor = "SET statement_timeout = '600s';\n"
    assert s.count(anchor) == 1
    ins = "-- AND p.success)  ★ 变异体：反向禁的关键词只出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m18_restore():
    restore_path(SCRIPT)


check("M1 ★ promote 自己的去重键探针被摘掉（★ 判据①）", True,
      "TestSection14395", m1_mut, m1_restore)
check("M2 ★ 父表探针的 is_final_success 被摘掉（★ 判据①）", True,
      "TestSection14395", m2_mut, m2_restore)
check("M3 ★★ 父表探针的键被换成 promote 自己的键（★ 判据①：★ 本轮第一次犯的错）", True,
      "TestSection14395", m3_mut, m3_restore)
check("M4 ★ 用 `success` 冒充 `is_final_success`（★ 判据①的反向禁）", True,
      "TestSection14395", m4_mut, m4_restore)
check("M5 ★ 8h 保留窗判据被改成 24h（★ 判据②）", True,
      "TestSection14395", m5_mut, m5_restore)
check("M6 ★ Go 侧保留窗常量被改（★ 判据②）", True,
      "TestSection14395", m6_mut, m6_restore)
check("M7 ★ Go 侧批大小被改（★ 判据②）", True,
      "TestSection14395", m7_mut, m7_restore)
check("M8 ★ 869 基线的排水判据被改（★ 判据②）", True,
      "TestSection14395", m8_mut, m8_restore)
check("M9 ★ 父表饿死的判据被摘掉（★ 判据③）", True,
      "TestSection14395", m9_mut, m9_restore)
check("M10 ★ 修法样板的 demote 步被摘掉（★ 判据④）", True,
      "TestSection14395", m10_mut, m10_restore)
check("M11 ★ v1 族退役的证据被摘掉（★ 判据④）", True,
      "TestSection14395", m11_mut, m11_restore)
check("M12 ★ 23505 的来源索引名被改（★ 判据④）", True,
      "TestSection14395", m12_mut, m12_restore)
check("M13 ★ 「剥注释后匹配」被摘掉（★ 判据⑤配套）", True,
      "TestSection14395", m13_mut, m13_restore)
check("M14 ★ 可执行面混进一条 DELETE（★ 判据⑤：只读守卫）", True,
      "TestSection14395", m14_mut, m14_restore)
check("M15 ★ §143.95 段的关键标识被改（★ 判据⑥）", True,
      "TestSection14395", m15_mut, m15_restore)
check("M16 ★ LESSONS §289 / AGENTS R139 编号被改（★ 判据⑥配套）", True,
      "TestSection14395", m16_mut, m16_restore)
check("M17 写语句只出现在注释里（★ 期望【仍然绿】：★ 判据查可执行面）", False,
      "TestSection14395", m17_mut, m17_restore)
check("M18 反向禁的关键词只出现在注释里（★ 期望【仍然绿】）", False,
      "TestSection14395", m18_mut, m18_restore)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))

env_after = env_fingerprint()
env_ok = True
for p in WATCHED:
    if env_after["md5"][p] != ENV_BEFORE["md5"][p]:
        print("❌ 环境指纹不一致：%s 的 md5 变了（★ 可能是并行会话的并发改动）" % p)
        env_ok = False
if env_after["status"] != ENV_BEFORE["status"]:
    print("⚠ 全仓 git status 在变异期间发生变化（★ 并行会话的并发改动）")
    env_ok = False
if env_ok:
    print("✅ 环境指纹一致：★ %d 个文件 md5 逐字未变 + 全仓 git status 未变" % len(WATCHED))

sys.exit(0 if (n_ok == len(results) and env_ok) else 1)
