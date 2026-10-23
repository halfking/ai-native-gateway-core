#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.91 存储审计门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14391_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 五条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M8）；
★   ④ 变异必须【语义变了但仍能跑/仍能编译】（★ 编译红 ≠ 断言红）；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）。
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
SCRIPT = os.path.join(ROOT, "scripts/audit/storage_hotspots.sql")

WATCHED = (RUNBOOK, LESSONS, AGENTS, SCRIPT)

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


def replace_in_segment(path, header, old, new):
    """★ 只在某个 `### ` 段内替换（★ 避免误伤其它段）。"""
    s = read(path)
    i = s.index(header)
    j = s.find("\n### ", i + 1)
    if j < 0:
        j = len(s)
    seg = s[i:j]
    n = seg.count(old)
    assert n > 0, "needle 在 %s 的 %s 段里出现 0 次：%r" % (path, header, old)
    write(path, s[:i] + seg.replace(old, new) + s[j:])
    return n


def restore_path(path):
    with open(path, "wb") as f:
        f.write(backups[path])


# ---------- M1：★ 三张「n_live_tup = 0」的表少了一张的独立 count（★ 判据③） ----------
def m1_mut():
    replace_all(SCRIPT,
                "SELECT count(*) AS real_rows FROM public.ursm_node_snapshot_min_legacy;\n",
                "",
                expect_n=1)
def m1_restore():
    restore_path(SCRIPT)


# ---------- M2：★ 内容体积口径被摘掉（★ 判据④）—— ★ 只剩存储体积 ⇒ 就是低报 ----------
def m2_mut():
    replace_all(SCRIPT, "length(outbound_body::text)", "pg_column_size(outbound_body)")
def m2_restore():
    restore_path(SCRIPT)


# ---------- M3：★ 可执行面里混进一条写语句（★ 判据⑤：只读守卫） ----------
def m3_mut():
    s = read(SCRIPT)
    write(SCRIPT, s.rstrip("\n") + "\nDELETE FROM public.session_summaries;\n")
def m3_restore():
    restore_path(SCRIPT)


# ---------- M4：★ §143.91 段的体积数被改（★ 判据⑥） ----------
def m4_mut():
    replace_in_segment(RUNBOOK, "### §143.91", "5,228", "5,229")
def m4_restore():
    restore_path(RUNBOOK)


# ---------- M5：★ §143.90 的订正横幅七被改（★ 判据⑦） ----------
def m5_mut():
    replace_all(RUNBOOK,
                "订正横幅七（2026-10-23 §143.91 轮）",
                "订正横幅七（2026-10-23 旧轮）",
                expect_n=1)
def m5_restore():
    restore_path(RUNBOOK)


# ---------- M6：★ LESSONS §285 编号被改（★ 判据⑧） ----------
def m6_mut():
    replace_all(LESSONS, "## §285", "## §286", expect_n=1)
def m6_restore():
    restore_path(LESSONS)


# ---------- M7：★ AGENTS R135 被改（★ 判据⑧） ----------
def m7_mut():
    replace_all(AGENTS, "R135 轮", "R136 轮", expect_n=1)
def m7_restore():
    restore_path(AGENTS)


# ---------- M8（★ 期望【仍然绿】）：★ 写语句只出现在【注释】里 ----------
def m8_mut():
    s = read(SCRIPT)
    anchor = "SET statement_timeout = '300s';\n"
    assert s.count(anchor) == 1
    ins = "-- DELETE FROM public.session_summaries;  ★ 变异体：写语句只出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m8_restore():
    restore_path(SCRIPT)


# ---------- M9：★ reltuples 口径被摘掉（★ 判据②：三口径少一） ----------
def m9_mut():
    replace_all(SCRIPT, "reltuples::bigint AS reltuples_est,", "")
def m9_restore():
    restore_path(SCRIPT)


# ---------- M10：★ n_live_tup 口径被摘掉（★ 判据②：三口径少一） ----------
def m10_mut():
    replace_all(SCRIPT, "       n_live_tup,\n", "")
def m10_restore():
    restore_path(SCRIPT)


# ---------- M11：★ 少一段（★ 判据①） ----------
def m11_mut():
    s = read(SCRIPT)
    old = "\\echo '=== 1/7:"
    assert s.count(old) == 1
    write(SCRIPT, s.replace(old, "-- " + old))
def m11_restore():
    restore_path(SCRIPT)


check("M1 ★ 三张「n_live_tup=0」的表少了一张独立 count（★ 判据③）", True,
      "TestSection14391", m1_mut, m1_restore)
check("M2 ★ 内容体积口径被摘掉（★ 判据④：★ 只剩存储体积）", True,
      "TestSection14391", m2_mut, m2_restore)
check("M3 ★ 可执行面混进一条 DELETE（★ 判据⑤：只读守卫）", True,
      "TestSection14391", m3_mut, m3_restore)
check("M4 ★ §143.91 段的 5,228 被改（★ 判据⑥）", True,
      "TestSection14391", m4_mut, m4_restore)
check("M5 ★ §143.90 的订正横幅七被改（★ 判据⑦）", True,
      "TestSection14391", m5_mut, m5_restore)
check("M6 ★ LESSONS §285 编号被改（★ 判据⑧）", True,
      "TestSection14391", m6_mut, m6_restore)
check("M7 ★ AGENTS R135 被改（★ 判据⑧）", True,
      "TestSection14391", m7_mut, m7_restore)
check("M8 写语句只出现在注释里（★ 期望【仍然绿】：★ 判据查可执行面）", False,
      "TestSection14391", m8_mut, m8_restore)
check("M9 ★ reltuples 口径被摘掉（★ 判据②：三口径少一）", True,
      "TestSection14391", m9_mut, m9_restore)
check("M10 ★ n_live_tup 口径被摘掉（★ 判据②：三口径少一）", True,
      "TestSection14391", m10_mut, m10_restore)
check("M11 ★ 少一段（★ 判据①）", True,
      "TestSection14391", m11_mut, m11_restore)

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
