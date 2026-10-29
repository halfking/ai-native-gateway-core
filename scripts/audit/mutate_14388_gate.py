#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.88 映射链门的变异台：★ 验证「门绿 ≠ 门有牙」—— ★ 改坏登记/事实后门必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14388_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 五条纪律（★ 与 §143.87 的变异台同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M9）；
★   ④ 变异必须【语义变了但仍能编译】⇒ ⇒ SQL 表名的字符串替换是安全的；
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
SCRIPT = os.path.join(ROOT, "scripts/audit/funnel_algebra.py")
READER = os.path.join(ROOT, "domains/session/v2/turn_reader.go")

WATCHED = (RUNBOOK, LESSONS, AGENTS, SCRIPT, READER)

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


def seg_replace(path, start_marker, end_marker, old, new):
    s = read(path)
    i = s.index(start_marker)
    j = s.index(end_marker, i) if end_marker else len(s)
    seg = s[i:j]
    assert seg.count(old) > 0, "needle %r 在 %s 的段里出现 0 次" % (old, path)
    seg2 = seg.replace(old, new)
    assert seg2 != seg, "替换没有生效"
    write(path, s[:i] + seg2 + s[j:])


# ---------- M1：§143.88 段的 180 被改（判据②） ----------
def m1_mut():
    seg_replace(RUNBOOK, "### §143.88", None, "180", "181")
def m1_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M2：§143.88 段的「A / 473」被改（判据⑧） ----------
def m2_mut():
    seg_replace(RUNBOOK, "### §143.88", None, "A / 473", "A / 474")
def m2_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M3：§143.88 段的「拿不到」被改（判据⑧） ----------
def m3_mut():
    seg_replace(RUNBOOK, "### §143.88", None, "拿不到", "拿得到")
def m3_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M4：§143.87 段的订正横幅五被改（判据⑤） ----------
def m4_mut():
    seg_replace(RUNBOOK, "### §143.87", "### §143.88",
                "订正横幅五（2026-10-23 §143.88 轮）", "订正横幅五（2026-10-23 旧轮）")
def m4_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M5：LESSONS §282 编号被改（判据⑥） ----------
def m5_mut():
    s = read(LESSONS)
    assert s.count("## §282") == 1
    write(LESSONS, s.replace("## §282", "## §283"))
def m5_restore():
    with open(LESSONS, "wb") as f:
        f.write(backups[LESSONS])


# ---------- M6：AGENTS R132 被改（判据⑥） ----------
def m6_mut():
    s = read(AGENTS)
    assert s.count("R132") == 1
    write(AGENTS, s.replace("R132", "R133"))
def m6_restore():
    with open(AGENTS, "wb") as f:
        f.write(backups[AGENTS])


# ---------- M7：代数脚本的 L1 命中层常量被改（判据⑦） ----------
def m7_mut():
    s = read(SCRIPT)
    assert "EXPECTED_L1HIT_LOWER = 180" in s
    write(SCRIPT, s.replace("EXPECTED_L1HIT_LOWER = 180", "EXPECTED_L1HIT_LOWER = 190"))
def m7_restore():
    with open(SCRIPT, "wb") as f:
        f.write(backups[SCRIPT])


# ---------- M8：★★ 腿① 改读 V1 表（★ 判据④ —— 本门最硬的一条） ----------
def m8_mut():
    s = read(READER)
    assert s.count("public.session_bodies_unified") >= 1
    write(READER, s.replace("public.session_bodies_unified", "public._request_logs"))
def m8_restore():
    with open(READER, "wb") as f:
        f.write(backups[READER])


# ---------- M9（★ 期望【仍然绿】）：★ 在 §143.88 段【之外】引用 180 ----------
def m9_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.88")
    ins = ("\n★ ★★ （★ 历史叙述：★ 「180 次」那一版曾写在别处，★ 口径已重算。）\n")
    write(RUNBOOK, s[:i] + ins + s[i:])
def m9_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


check("M1 §143.88 段的 180 被改（判据②）", True,
      "TestSection14388", m1_mut, m1_restore)
check("M2 §143.88 段的「A / 473」被改（判据⑧）", True,
      "TestSection14388", m2_mut, m2_restore)
check("M3 §143.88 段的「拿不到」被改（判据⑧）", True,
      "TestSection14388", m3_mut, m3_restore)
check("M4 §143.87 段的订正横幅五被改（判据⑤）", True,
      "TestSection14388", m4_mut, m4_restore)
check("M5 LESSONS §282 编号被改（判据⑥）", True,
      "TestSection14388", m5_mut, m5_restore)
check("M6 AGENTS R132 被改（判据⑥）", True,
      "TestSection14388", m6_mut, m6_restore)
check("M7 代数脚本的 L1 命中层常量被改（判据⑦）", True,
      "TestSection14388", m7_mut, m7_restore)
check("M8 腿① 改读 V1 表（★ 判据④：★ 前提 A 会立即失效 ⇒ ⇒ 门必须红）", True,
      "TestSection14388", m8_mut, m8_restore)
check("M9 在 §143.88 段【之外】引用 180（★ 期望【仍然绿】：★ 判据有作用域）", False,
      "TestSection14388", m9_mut, m9_restore)

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
