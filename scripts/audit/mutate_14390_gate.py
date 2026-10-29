#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.90 求交器门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14390_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 五条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M8）；
★   ④ 变异必须【语义变了但仍能跑/仍能编译】；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）。
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
SCRIPT = os.path.join(ROOT, "scripts/audit/cross_source_intersect.py")

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


def replace_once(path, old, new):
    s = read(path)
    assert s.count(old) == 1, "needle 在 %s 里出现 %d 次（期望 1）" % (path, s.count(old))
    write(path, s.replace(old, new))


# ---------- M1：★ body_size 过滤被摘掉（★ 判据③） ----------
def m1_mut():
    replace_once(SCRIPT,
                 "if require_zero_body and body_size not in (0, None):",
                 "if False and require_zero_body and body_size not in (0, None):")
def m1_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M2：★ 多重集交被换成集合交（★ 判据④） ----------
def m2_mut():
    replace_once(SCRIPT, "return a & b", "return Counter(set(a) & set(b))")
def m2_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M3：自检入口被删（判据②） ----------
def m3_mut():
    s = read(SCRIPT)
    assert s.count('"--self-check"') == 1
    write(SCRIPT, s.replace('"--self-check"', '"--selfcheck"'))
def m3_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M4：§143.90 段的 220 被改（判据⑥） ----------
def m4_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.90")
    seg = s[i:]
    seg2 = seg.replace("220", "221")
    assert seg2 != seg
    write(RUNBOOK, s[:i] + seg2)
def m4_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M5：§143.90 段的 286 被改（判据⑥） ----------
def m5_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.90")
    seg = s[i:]
    seg2 = seg.replace("286", "287")
    assert seg2 != seg
    write(RUNBOOK, s[:i] + seg2)
def m5_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M6：§143.84 的订正横幅六被删（判据⑥） ----------
def m6_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.84")
    j = s.index("### §143.85")
    seg = s[i:j]
    seg2 = seg.replace("订正横幅六（2026-10-23 §143.90 轮）", "订正横幅六（2026-10-23 旧轮）")
    assert seg2 != seg
    write(RUNBOOK, s[:i] + seg2 + s[j:])
def m6_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M7：LESSONS §284 编号被改（判据⑥） ----------
def m7_mut():
    s = read(LESSONS)
    assert s.count("## §284") == 1
    write(LESSONS, s.replace("## §284", "## §285"))
def m7_restore():
    with open(LESSONS, "wb") as f:
        f.write(backups[LESSONS])


# ---------- M8（★ 期望【仍然绿】）：★ 判据文本只出现在【注释】里 ----------
def m8_mut():
    s = read(SCRIPT)
    anchor = "def collect(path: str, mark: str, fmt: str, since, until, require_zero_body: bool = False) -> Counter:\n"
    assert s.count(anchor) == 1
    ins = "    # 变异体：★ if require_zero_body and body_size not in (0, None): 出现在注释里\n"
    write(SCRIPT, s.replace(anchor, anchor + ins))
def m8_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


check("M1 ★ body_size 过滤被摘掉（★ 判据③：★ 量具级错误①）", True,
      "TestSection14390", m1_mut, m1_restore)
check("M2 ★ 多重集交被换成集合交（★ 判据④）", True,
      "TestSection14390", m2_mut, m2_restore)
check("M3 自检入口被删（判据②）", True,
      "TestSection14390", m3_mut, m3_restore)
check("M4 §143.90 段的 220 被改（判据⑥）", True,
      "TestSection14390", m4_mut, m4_restore)
check("M5 §143.90 段的 286 被改（判据⑥）", True,
      "TestSection14390", m5_mut, m5_restore)
check("M6 §143.84 的订正横幅六被改（判据⑥）", True,
      "TestSection14390", m6_mut, m6_restore)
check("M7 LESSONS §284 编号被改（判据⑥）", True,
      "TestSection14390", m7_mut, m7_restore)
check("M8 判据文本只出现在注释里（★ 期望【仍然绿】：★ 判据剥注释）", False,
      "TestSection14390", m8_mut, m8_restore)

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
