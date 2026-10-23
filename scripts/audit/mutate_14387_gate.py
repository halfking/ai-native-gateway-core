#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.87 登记门的变异台：★ 验证「门绿 ≠ 门有牙」—— ★ 改坏登记后门必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14387_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 五条纪律（本脚本已内建）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ ★★ 只改第一处会漏 ⇒ ⇒ 门仍然绿 ⇒ ⇒ 假阴性）；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M11 / M12）；
★   ④ 变异必须【语义变了但仍能编译/能跑】⇒ ⇒ Python 侧改名而不是删定义；
★   ⑤ ★★★ 【环境指纹对拍】（★ 本轮实测踩到的坑）：★ 共享工作树里 ⇒ ⇒ 并行会话的
★      并发改动会被【误读成「门没牙」】⇒ ⇒ 跑完对拍「全仓 git status + 本脚本动的 4 个文件
★      的 md5」⇒ ⇒ 不一致 ⇒ ⇒ 报【环境被并发改动】（★ exit 1），★ 而不把读数当门的问题。
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
SCRIPT = os.path.join(ROOT, "scripts/audit/funnel_algebra.py")

backups = {}


# ★★★★★★ 环境指纹（★ 本轮实测踩到的一个坑）：★ 共享工作树里跑变异台 ⇒
# ★ ⇒ 并行会话的并发改动会被【误读成「门没牙」】。
# ★ ⇒ ⇒ 治法 = 记录「全仓 git status + 本脚本要动的 4 个文件的 md5」⇒ ⇒
# ★ ⇒ ⇒ 跑完对拍 ⇒ ⇒ 不一致 ⇒ ⇒ 报「环境被并发改动」，★ 而不是把读数当门的问题。
def git_status():
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                       capture_output=True, text=True)
    return p.stdout


def file_md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def env_fingerprint():
    return {
        "status": git_status(),
        "md5": {p: file_md5(p) for p in (RUNBOOK, LESSONS, AGENTS, SCRIPT)},
    }


ENV_BEFORE = env_fingerprint()


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
    for p in (RUNBOOK, LESSONS, AGENTS, SCRIPT):
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


def seg_replace(path, start_marker, end_marker, old, new, count_all=True):
    """在 runbook 的【某段】里替换文本（★ start_marker 之后、end_marker 之前）。"""
    s = read(path)
    i = s.index(start_marker)
    j = s.index(end_marker, i) if end_marker else len(s)
    seg = s[i:j]
    n = seg.count(old)
    assert n > 0, "needle %r 在 %s 的段里出现 0 次" % (old, path)
    if count_all:
        seg2 = seg.replace(old, new)
    else:
        seg2 = seg.replace(old, new, 1)
    assert seg2 != seg, "替换没有生效"
    write(path, s[:i] + seg2 + s[j:])


# ---------- M1：把 §143.87 段里的 669 全部改掉（判据② + 判据⑦） ----------
def m1_mut():
    seg_replace(RUNBOOK, "### §143.87", None, "669", "670")
def m1_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M2：把 §143.87 段里的「s / 293」全部改掉（判据③） ----------
def m2_mut():
    seg_replace(RUNBOOK, "### §143.87", None, "s / 293", "s/293")
def m2_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M3：把 §143.87 段里的 61.04 改掉（判据②） ----------
def m3_mut():
    seg_replace(RUNBOOK, "### §143.87", None, "61.04", "61.40")
def m3_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M4：删掉 §143.85 段的订正横幅四（判据④） ----------
def m4_mut():
    seg_replace(RUNBOOK, "### §143.85", "### §143.86",
                "订正横幅四（2026-10-23 §143.87 轮）", "订正横幅四（2026-10-23 旧轮）")
def m4_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M5：把 §143.85 横幅里的「≥189 次」全部改掉（判据④） ----------
def m5_mut():
    seg_replace(RUNBOOK, "### §143.85", "### §143.86", "≥189 次", "≥190 次")
def m5_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M6：LESSONS 的 §278 编号被改（判据⑤） ----------
def m6_mut():
    s = read(LESSONS)
    assert s.count("## §278") == 1
    write(LESSONS, s.replace("## §278", "## §279"))
def m6_restore():
    with open(LESSONS, "wb") as f:
        f.write(backups[LESSONS])


# ---------- M7：AGENTS 的 R130 被改（判据⑥） ----------
def m7_mut():
    s = read(AGENTS)
    assert s.count("R130") == 1
    write(AGENTS, s.replace("R130", "R131"))
def m7_restore():
    with open(AGENTS, "wb") as f:
        f.write(backups[AGENTS])


# ---------- M8：脚本常量与文档漂移（判据⑦） ----------
def m8_mut():
    s = read(SCRIPT)
    assert "EXPECTED_LEG1_CALLS = 669" in s
    write(SCRIPT, s.replace("EXPECTED_LEG1_CALLS = 669", "EXPECTED_LEG1_CALLS = 670"))
def m8_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M9：脚本少一个关键常量（判据⑦） ----------
def m9_mut():
    s = read(SCRIPT)
    # ★★★ 改名而不是删除 ⇒ ⇒ 脚本仍能跑（★ 变异要「语义变了但仍能跑」）。
    s = s.replace("EXPECTED_SVC_PRECISE", "EXPECTED_SVC_PRECISE_ALT")
    write(SCRIPT, s)
def m9_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M10：脚本的自检入口被删（判据⑧） ----------
def m10_mut():
    s = read(SCRIPT)
    assert s.count("--self-check") == 2
    write(SCRIPT, s.replace("--self-check", "--selfcheck"))
def m10_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M11（★ 期望【仍然绿】）：★ 在 §143.87 段【之外】加一句含「189 / 不成立」的文字 ----------
def m11_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.87")
    ins = ("\n★ ★★ （★ 历史叙述：★ 「≥189 次」那一版曾写在 §143.85.4，★ 已不成立。）\n")
    write(RUNBOOK, s[:i] + ins + s[i:])
def m11_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M12（★ 期望【仍然绿】）：★ 在 §143.87 段【之内】引用被推翻的数字（★ 原文保留纪律） ----------
def m12_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.87")
    ins = ("\n★ ★★ （★ 原文保留：★ §143.85.4 的「≥189 次、≥64.5%」作为历史叙述保留于此。）\n")
    write(RUNBOOK, s[:i] + ins + s[i:])
def m12_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


check("M1 §143.87 段的 669 被改（判据②⑦）", True,
      "TestSection14387", m1_mut, m1_restore)
check("M2 §143.87 段的「s / 293」被改（判据③）", True,
      "TestSection14387", m2_mut, m2_restore)
check("M3 §143.87 段的 61.04 被改（判据②）", True,
      "TestSection14387", m3_mut, m3_restore)
check("M4 §143.85 段的订正横幅四被改（判据④）", True,
      "TestSection14387", m4_mut, m4_restore)
check("M5 §143.85 横幅的「≥189 次」被改（判据④）", True,
      "TestSection14387", m5_mut, m5_restore)
check("M6 LESSONS §278 编号被改（判据⑤）", True,
      "TestSection14387", m6_mut, m6_restore)
check("M7 AGENTS R130 被改（判据⑥）", True,
      "TestSection14387", m7_mut, m7_restore)
check("M8 脚本常量与文档漂移（判据⑦）", True,
      "TestSection14387", m8_mut, m8_restore)
check("M9 脚本少一个关键常量（判据⑦）", True,
      "TestSection14387", m9_mut, m9_restore)
check("M10 脚本的自检入口被删（判据⑧）", True,
      "TestSection14387", m10_mut, m10_restore)
check("M11 在 §143.87 段【之外】加含 189 的文字（★ 期望【仍然绿】：★ 判据有作用域）", False,
      "TestSection14387", m11_mut, m11_restore)
check("M12 在 §143.87 段【之内】引用被推翻的数字（★ 期望【仍然绿】：★ 原文保留）", False,
      "TestSection14387", m12_mut, m12_restore)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))

# ★★★★★★ 环境指纹对拍（★ 纪律⑤）：★ 共享工作树里 ⇒ ⇒ 并行会话的并发改动
# ★ ⇒ ⇒ 会把读数污染成「门没牙」⇒ ⇒ 这里判「环境变没变」⇒ ⇒ 而不是替门背锅。
env_after = env_fingerprint()
env_ok = True
for p in (RUNBOOK, LESSONS, AGENTS, SCRIPT):
    if env_after["md5"][p] != ENV_BEFORE["md5"][p]:
        print("❌ 环境指纹不一致：%s 的 md5 变了（★ 可能是并行会话的并发改动）" % p)
        env_ok = False
if env_after["status"] != ENV_BEFORE["status"]:
    print("⚠ 全仓 git status 在变异期间发生变化（★ 并行会话的并发改动）：")
    print("  --- 变异前 ---")
    print(ENV_BEFORE["status"].rstrip() or "  (clean)")
    print("  --- 变异后 ---")
    print(env_after["status"].rstrip() or "  (clean)")
    env_ok = False
if env_ok:
    print("✅ 环境指纹一致：★ 4 个文件 md5 逐字未变 + 全仓 git status 未变")

sys.exit(0 if (n_ok == len(results) and env_ok) else 1)
