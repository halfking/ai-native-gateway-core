#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.94 session_turns 族门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14394_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 六条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M13）；
★   ④ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑥ 变异必须落在【判据实际读到的那一处】。
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

RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
SCRIPT = os.path.join(ROOT, "scripts/audit/verify_session_turns_column_decomposition.sql")

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


# ---------- M1：★ `compression_meta` 的体积口径被摘掉（★ 判据①） ----------
def m1_mut():
    replace_all(SCRIPT,
                "sum(pg_column_size(compression_meta)) AS compression_meta_bytes",
                "sum(pg_column_size(compression_meta)) AS compression_meta_bytes_x",
                expect_n=1)
def m1_restore():
    restore_path(SCRIPT)


# ---------- M2：★ `digest` 的体积口径被摘掉（★ 判据①） ----------
def m2_mut():
    replace_all(SCRIPT,
                "sum(pg_column_size(digest))           AS digest_bytes",
                "sum(pg_column_size(digest))           AS digest_bytes_x",
                expect_n=1)
def m2_restore():
    restore_path(SCRIPT)


# ---------- M3：★ `tools` 的体积口径被摘掉（★ 判据①：★ 少一列就退回单一口径） ----------
def m3_mut():
    replace_all(SCRIPT,
                "sum(pg_column_size(tools))            AS tools_bytes",
                "sum(pg_column_size(tools))            AS tools_bytes_x",
                expect_n=1)
def m3_restore():
    restore_path(SCRIPT)


# ---------- M4：★ 「逐轮重写」这条尺子被摘掉（★ 判据②） ----------
def m4_mut():
    replace_all(SCRIPT,
                "lag(pg_column_size(compression_meta)) OVER (PARTITION BY session_id ORDER BY turn_no)",
                "lag(pg_column_size(compression_meta)) OVER (PARTITION BY session_id ORDER BY turn_no_x)",
                expect_n=1)
def m4_restore():
    restore_path(SCRIPT)


# ---------- M5：★ 「大对象规模」这条尺子被摘掉（★ 判据②） ----------
def m5_mut():
    replace_all(SCRIPT,
                "count(*) AS big_sessions",
                "count(*) AS big_sessions_x",
                expect_n=1)
    replace_all(SCRIPT,
                "sum(meta_bytes) AS big_meta_bytes",
                "sum(meta_bytes) AS big_meta_bytes_x",
                expect_n=1)
def m5_restore():
    restore_path(SCRIPT)


# ---------- M6：★ 大对象的阈值判据被改（★ 判据②） ----------
def m6_mut():
    replace_all(SCRIPT,
                "pg_column_size(compression_meta) > 8192",
                "pg_column_size(compression_meta) > 8192_x",
                expect_n=2)
def m6_restore():
    restore_path(SCRIPT)


# ---------- M7：★ 孤儿表判据的【子表列名】被改（★ 判据③） ----------
def m7_mut():
    replace_all(SCRIPT,
                "LEFT JOIN pg_inherits i ON i.inhrelid = c.oid",
                "LEFT JOIN pg_inherits i ON i.inhrelid_x = c.oid",
                expect_n=1)
def m7_restore():
    restore_path(SCRIPT)


# ---------- M8：★ 孤儿结论的字面量被改（★ 判据③） ----------
def m8_mut():
    replace_all(SCRIPT,
                "'ORPHAN-not-under-parent'",
                "'ORPHAN-not-under-parent-x'",
                expect_n=1)
    replace_all(SCRIPT,
                "'parent-table'",
                "'parent-table-x'",
                expect_n=1)
def m8_restore():
    restore_path(SCRIPT)


# ---------- M9：★ 可执行面里混进一条写语句（★ 判据④：只读守卫） ----------
def m9_mut():
    s = read(SCRIPT)
    write(SCRIPT, s.rstrip("\n") + "\nDELETE FROM public.session_turns_2026_09;\n")
def m9_restore():
    restore_path(SCRIPT)


# ---------- M10：★ §143.94 段的 compression_meta 体积被改（★ 判据⑤） ----------
def m10_mut():
    replace_in_segment(RUNBOOK, "### §143.94", "569,396,910", "569,396,911", expect_n=1)
def m10_restore():
    restore_path(RUNBOOK)


# ---------- M11：★ §143.94 段的 digest 体积被改（★ 判据⑤） ----------
def m11_mut():
    replace_in_segment(RUNBOOK, "### §143.94", "533,160,775", "533,160,776", expect_n=1)
def m11_restore():
    restore_path(RUNBOOK)


# ---------- M12：★ LESSONS §288 / AGENTS R138 编号被改（★ 判据⑤） ----------
def m12_mut():
    replace_all(LESSONS, "## §288", "## §289", expect_n=1)
    replace_all(AGENTS, "R138 轮", "R139 轮", expect_n=1)
def m12_restore():
    restore_path(LESSONS)
    restore_path(AGENTS)


# ---------- M13（★ 期望【仍然绿】）：★ 写语句只出现在【注释】里 ----------
def m13_mut():
    s = read(SCRIPT)
    anchor = "SET statement_timeout = '600s';\n"
    assert s.count(anchor) == 1
    ins = "-- DELETE FROM public.session_turns_2026_09;  ★ 变异体：写语句只出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m13_restore():
    restore_path(SCRIPT)


check("M1 ★ `compression_meta` 的体积口径被摘掉（★ 判据①）", True,
      "TestSection14394", m1_mut, m1_restore)
check("M2 ★ `digest` 的体积口径被摘掉（★ 判据①）", True,
      "TestSection14394", m2_mut, m2_restore)
check("M3 ★ `tools` 的体积口径被摘掉（★ 判据①）", True,
      "TestSection14394", m3_mut, m3_restore)
check("M4 ★ 「逐轮重写」这条尺子被摘掉（★ 判据②）", True,
      "TestSection14394", m4_mut, m4_restore)
check("M5 ★ 「大对象规模」这条尺子被摘掉（★ 判据②）", True,
      "TestSection14394", m5_mut, m5_restore)
check("M6 ★ 大对象的阈值判据被改（★ 判据②）", True,
      "TestSection14394", m6_mut, m6_restore)
check("M7 ★ 孤儿表判据的【子表列名】被改（★ 判据③）", True,
      "TestSection14394", m7_mut, m7_restore)
check("M8 ★ 孤儿结论的字面量被改（★ 判据③）", True,
      "TestSection14394", m8_mut, m8_restore)
check("M9 ★ 可执行面混进一条 DELETE（★ 判据④：只读守卫）", True,
      "TestSection14394", m9_mut, m9_restore)
check("M10 ★ §143.94 段的 compression_meta 体积被改（★ 判据⑤）", True,
      "TestSection14394", m10_mut, m10_restore)
check("M11 ★ §143.94 段的 digest 体积被改（★ 判据⑤）", True,
      "TestSection14394", m11_mut, m11_restore)
check("M12 ★ LESSONS §288 / AGENTS R138 编号被改（★ 判据⑤）", True,
      "TestSection14394", m12_mut, m12_restore)
check("M13 写语句只出现在注释里（★ 期望【仍然绿】：★ 判据查可执行面）", False,
      "TestSection14394", m13_mut, m13_restore)

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
