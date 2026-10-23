#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.92 正文族按列拆解门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14392_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 六条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M12）；
★   ④ 变异必须【语义变了但仍能跑/仍能编译】（★ 编译红 ≠ 断言红）；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑥ 变异必须落在【判据实际读到的那一处】（★ 本轮的判据查的是【可执行面】）。
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
SCRIPT = os.path.join(ROOT, "scripts/audit/verify_session_bodies_column_decomposition.sql")

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


# ---------- M1：★ `request_delta` 列被摘掉（★ 判据①：★ 逐列分解不许退化成单一口径） ----------
def m1_mut():
    replace_all(SCRIPT,
                "       sum(pg_column_size(request_delta))  AS req_delta_bytes,\n",
                "",
                expect_n=1)
def m1_restore():
    restore_path(SCRIPT)


# ---------- M2：★ 三列并列被拆开（★ 判据①：★ 必须一条 select 里并列） ----------
def m2_mut():
    replace_all(SCRIPT,
                "       sum(pg_column_size(response_delta)) AS resp_delta_bytes,\n",
                "       0 AS resp_delta_bytes,\n",
                expect_n=1)
def m2_restore():
    restore_path(SCRIPT)


# ---------- M3：★ 「重复字节占比」这把尺子被摘掉（★ 判据②：★ 新的尺子必须还在） ----------
def m3_mut():
    s = read(SCRIPT)
    old = "         lag(request_delta) OVER (PARTITION BY session_id ORDER BY turn_no) AS prev\n"
    n = s.count(old)
    assert n == 3, "lag needle 出现 %d 次（期望 3）" % n
    write(SCRIPT, s.replace(old, "         NULL::jsonb AS prev\n"))
def m3_restore():
    restore_path(SCRIPT)


# ---------- M4：★ prev_share 的两个分子分母被摘掉一个（★ 判据②） ----------
def m4_mut():
    replace_all(SCRIPT,
                "         pg_column_size(prev)          AS prev_bytes\n",
                "         0::integer                    AS prev_bytes\n",
                expect_n=2)
def m4_restore():
    restore_path(SCRIPT)


# ---------- M5：★ 按轮次数分桶的常量被摘掉（★ 判据②：★ 族级三分法） ----------
def m5_mut():
    replace_all(SCRIPT, "'1_ge10_turns'", "'1_many_turns'", expect_n=1)
def m5_restore():
    restore_path(SCRIPT)


# ---------- M6：★ 内容级口径（★ `@>`）被摘掉（★ 判据②配套：★ 两把尺子必须并排） ----------
def m6_mut():
    replace_all(SCRIPT, "AND request_delta @> prev) AS superset_pairs,",
                "AND false) AS superset_pairs,", expect_n=1)
def m6_restore():
    restore_path(SCRIPT)


# ---------- M7：★ `avg_new_msgs` 被摘掉（★ 判据②配套：★ `@>` 失明时靠它） ----------
def m7_mut():
    replace_all(SCRIPT, "AS avg_new_msgs", "AS avg_new_msgs_x", expect_n=1)
def m7_restore():
    restore_path(SCRIPT)


# ---------- M8：★ 可执行面里混进一条写语句（★ 判据③：只读守卫） ----------
def m8_mut():
    s = read(SCRIPT)
    write(SCRIPT, s.rstrip("\n") + "\nDELETE FROM public.session_summaries;\n")
def m8_restore():
    restore_path(SCRIPT)


# ---------- M9：★ §143.92 段的重复字节数被改（★ 判据④） ----------
# ★ ★★ 注意：★ 「1,623,849,268」在 §143.92 段里【合法出现 4 次】（★ 族级表 / 体积三分表 /
# ★ ★★ 口径并列表 / 诚实边界）⇒ ⇒ 所以 needle 必须【带上下文】才唯一；
# ★ ★★ 而变异只改【其中一处】⇒ ⇒ 用 count=1 的 replace（★ 只替第一次出现）。
def m9_mut():
    # ★ 真正要改的：族级表里 ≥10 轮那一行的「上一轮字节」。
    replace_in_segment(RUNBOOK, "### §143.92",
                       "| **≥10 轮** | **229** | 27,397 | **1,649,078,294** | 1,623,849,268 | **98.5%** | 0.958 |",
                       "| **≥10 轮** | **229** | 27,397 | **1,649,078,294** | 1,623,849,269 | **98.5%** | 0.958 |",
                       expect_n=1)
def m9_restore():
    restore_path(RUNBOOK)


# ---------- M10：★ §143.91 的订正横幅八被改（★ 判据④配套） ----------
def m10_mut():
    replace_all(RUNBOOK,
                "订正横幅八（2026-10-23 §143.92 轮）",
                "订正横幅八（2026-10-23 旧轮）",
                expect_n=1)
def m10_restore():
    restore_path(RUNBOOK)


# ---------- M11：★ §143.90 的订正横幅九被改（★ 判据④配套） ----------
def m11_mut():
    replace_all(RUNBOOK,
                "订正横幅九（2026-10-23 §143.92 轮）",
                "订正横幅九（2026-10-23 旧轮）",
                expect_n=1)
def m11_restore():
    restore_path(RUNBOOK)


# ---------- M12：★ LESSONS §286 / AGENTS R136 编号被改（★ 判据⑤） ----------
def m12_mut():
    replace_all(LESSONS, "## §286", "## §287", expect_n=1)
    replace_all(AGENTS, "R136 轮", "R137 轮", expect_n=1)
def m12_restore():
    restore_path(LESSONS)
    restore_path(AGENTS)


# ---------- M13（★ 期望【仍然绿】）：★ 写语句只出现在【注释】里 ----------
def m13_mut():
    s = read(SCRIPT)
    anchor = "SET statement_timeout = '600s';\n"
    assert s.count(anchor) == 1
    ins = "-- DELETE FROM public.session_summaries;  ★ 变异体：写语句只出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m13_restore():
    restore_path(SCRIPT)


# ---------- M14：★ 少一段（★ 判据①：★ 被注释掉的 `\echo` 不算一段 ⇒ ⇒ 门必须红） ----------
# ★ ★★★ 这一条【曾经被我写错方向】：★ 我以为「注释掉一段仍是 10 个 echo」⇒ ⇒ 期望绿。
# ★ ★★ 但实测：★ 门的段数判据查的是【非注释行里的 `\echo`】⇒ ⇒ 注释掉一段 ⇒ ⇒ **9 段** ⇒ ⇒ 门红。
# ★ ⇒ ⇒ ★★★ ⇒ **判据的边界必须实测、★ 不能靠推理**（★ 同 §143.91 的 M11）。
def m14_mut():
    s = read(SCRIPT)
    old = "\\echo '=== 10/10:"
    assert s.count(old) == 1
    write(SCRIPT, s.replace(old, "-- " + old))
def m14_restore():
    restore_path(SCRIPT)


check("M1 ★ `request_delta` 列被摘掉（★ 判据①：★ 逐列分解）", True,
      "TestSection14392", m1_mut, m1_restore)
check("M2 ★ 三列并列被拆开（★ 判据①：★ 必须一条 select 并列）", True,
      "TestSection14392", m2_mut, m2_restore)
check("M3 ★ 「重复字节占比」这把尺子被摘掉（★ 判据②）", True,
      "TestSection14392", m3_mut, m3_restore)
check("M4 ★ prev_share 的分子分母被摘掉一个（★ 判据②）", True,
      "TestSection14392", m4_mut, m4_restore)
check("M5 ★ 按轮次数分桶的常量被摘掉（★ 判据②：★ 族级三分法）", True,
      "TestSection14392", m5_mut, m5_restore)
check("M6 ★ 内容级 `@>` 口径被摘掉（★ 判据②配套）", True,
      "TestSection14392", m6_mut, m6_restore)
check("M7 ★ `avg_new_msgs` 被摘掉（★ 判据②配套）", True,
      "TestSection14392", m7_mut, m7_restore)
check("M8 ★ 可执行面混进一条 DELETE（★ 判据③：只读守卫）", True,
      "TestSection14392", m8_mut, m8_restore)
check("M9 ★ §143.92 段的重复字节数被改（★ 判据④）", True,
      "TestSection14392", m9_mut, m9_restore)
check("M10 ★ §143.91 的订正横幅八被改（★ 判据④配套）", True,
      "TestSection14392", m10_mut, m10_restore)
check("M11 ★ §143.90 的订正横幅九被改（★ 判据④配套）", True,
      "TestSection14392", m11_mut, m11_restore)
check("M12 ★ LESSONS §286 / AGENTS R136 编号被改（★ 判据⑤）", True,
      "TestSection14392", m12_mut, m12_restore)
check("M13 写语句只出现在注释里（★ 期望【仍然绿】：★ 判据查可执行面）", False,
      "TestSection14392", m13_mut, m13_restore)
check("M14 少一段（★ 判据①：★ 被注释掉的 `\\echo` 不算一段 ⇒ ⇒ 门必须红）", True,
      "TestSection14392", m14_mut, m14_restore)

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
