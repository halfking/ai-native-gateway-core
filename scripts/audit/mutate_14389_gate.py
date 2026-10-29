#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.89 埋点门的变异台：★ 验证「门绿 ≠ 门有牙」—— ★ 改坏埋点后门必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14389_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 五条纪律（★ 与前两轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M8）；
★   ④ 变异必须【语义变了但仍能编译】⇒ ⇒ M4 连 import 一起改；
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
CARRIER = os.path.join(ROOT, "domains/hooks/compression/l3_reached_log.go")
UNIT = os.path.join(ROOT, "domains/hooks/compression/l3_reached_log_test.go")
CACHE = os.path.join(ROOT, "domains/hooks/compression/session_cache.go")

WATCHED = (RUNBOOK, LESSONS, AGENTS, CARRIER, UNIT, CACHE)

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


# ---------- M1：埋点调用被摘掉（判据③） ----------
def m1_mut():
    replace_once(CACHE, "\tLogL3Reached(ctx, gwSessionID, tenantID)\n", "")
def m1_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


# ---------- M2：★ 埋点被挪到 G-2 守卫【之前】（★ 判据③：★ 位置退化） ----------
def m2_mut():
    s = read(CACHE)
    probe = "\tLogL3Reached(ctx, gwSessionID, tenantID)\n"
    assert s.count(probe) == 1
    s = s.replace(probe, "")
    anchor = "func (c *SessionCache) loadFromLegacyDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {\n"
    assert s.count(anchor) == 1
    s = s.replace(anchor, anchor + probe)
    write(CACHE, s)
def m2_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


# ---------- M3：开关常量改名（判据①） ----------
def m3_mut():
    replace_once(CARRIER, "ENABLE_SESSION_CACHE_L3_REACHED_LOG",
                 "ENABLE_SESSION_CACHE_L3_REACHED_LOG_V2")
def m3_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


# ---------- M4：★ 换回 kill-switch 型 API（★ 判据②：★ 默认会变成「开」） ----------
def m4_mut():
    s = read(CARRIER)
    s = s.replace('\t"sync"\n)', '\t"sync"\n\n\t"github.com/kaixuan/llm-gateway-go/settings"\n)')
    s = s.replace("if !l3ReachedLogEnabled() {",
                  'if !settings.IsEnabled("session_cache_l3_reached_log") {')
    write(CARRIER, s)
def m4_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


# ---------- M5：默认关被改成默认开（判据②） ----------
def m5_mut():
    s = read(CARRIER)
    old = '\tif v == "" {\n\t\treturn false // ★ 默认关\n\t}'
    assert s.count(old) == 1
    write(CARRIER, s.replace(old, '\tif v == "" {\n\t\treturn true // ★ 变异体\n\t}'))
def m5_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


# ---------- M6：同包单测被改名（判据⑤） ----------
def m6_mut():
    replace_once(UNIT, "func TestL3ReachedLogDefaultIsOff(",
                 "func TestL3ReachedLogDefaultIsOffX(")
def m6_restore():
    with open(UNIT, "wb") as f:
        f.write(backups[UNIT])


# ---------- M7：文档登记里的开关名被改（判据⑥） ----------
def m7_mut():
    s = read(RUNBOOK)
    i = s.index("### §143.89")
    seg = s[i:]
    seg2 = seg.replace("ENABLE_SESSION_CACHE_L3_REACHED_LOG", "L3_REACHED_LOG")
    assert seg2 != seg
    write(RUNBOOK, s[:i] + seg2)
def m7_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M8（★ 期望【仍然绿】）：★ 注释里出现埋点名（★ 判据查剥注释后的代码面） ----------
def m8_mut():
    s = read(CACHE)
    anchor = "func (c *SessionCache) loadFromLegacyDB(ctx context.Context, tenantID, gwSessionID string) (*SessionState, []byte, error) {\n"
    assert s.count(anchor) == 1
    ins = "\t// 变异体：★ LogL3Reached(ctx, gwSessionID, tenantID) 出现在注释里 ⇒ ⇒ 不应触发红\n"
    write(CACHE, s.replace(anchor, anchor + ins))
def m8_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


check("M1 埋点调用被摘掉（判据③）", True,
      "TestSection14389", m1_mut, m1_restore)
check("M2 ★ 埋点被挪到守卫之前（★ 判据③：★ 位置退化）", True,
      "TestSection14389", m2_mut, m2_restore)
check("M3 开关常量改名（判据①）", True,
      "TestSection14389", m3_mut, m3_restore)
check("M4 换回 kill-switch 型 API（★ 判据②）", True,
      "TestSection14389", m4_mut, m4_restore)
check("M5 默认关被改成默认开（判据②）", True,
      "TestSection14389", m5_mut, m5_restore)
check("M6 同包单测被改名（判据⑤）", True,
      "TestSection14389", m6_mut, m6_restore)
check("M7 文档登记里的开关名被改（判据⑥）", True,
      "TestSection14389", m7_mut, m7_restore)
check("M8 注释里出现埋点名（★ 期望【仍然绿】：★ 判据查剥注释代码面）", False,
      "TestSection14389", m8_mut, m8_restore)

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
