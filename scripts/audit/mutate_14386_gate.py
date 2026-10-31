#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.86 实现门的变异台：★ 验证「门绿 ≠ 门有牙」—— ★ 改坏实现后门必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14386_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 三条纪律（本脚本已内建）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须【编译干净】—— ★★ 【编译红 ≠ 断言红】★ ⇒ ⇒ 每个变异都挑【语义变了但仍能编译】的写法；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M9）。
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

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

CARRIER = os.path.join(ROOT, "domains/hooks/compression/auto_created.go")
UNIT = os.path.join(ROOT, "domains/hooks/compression/auto_created_test.go")
CACHE = os.path.join(ROOT, "domains/hooks/compression/session_cache.go")
HANDLER = os.path.join(ROOT, "domains/streaming/handler.go")
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)

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
    for p in (CARRIER, UNIT, CACHE, HANDLER, LESSONS, AGENTS, RUNBOOK):
        snap(p)
    try:
        mutate()
        red, out = run_gate(pattern)
        ok = (red == expect_red)
        mark = "✔" if ok else "❌"
        if expect_red:
            print(("🔴 " if red else "🟢 ") + name + (" ⇒ 变红 " + mark if red else " ⇒ 仍然绿 " + mark))
        else:
            print(("🟢 " if not red else "🔴 ") + name + (" ⇒ 仍然绿 " + mark if not red else " ⇒ 变红 " + mark))
        if not ok:
            print(out[-1800:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


def replace_once(path, old, new):
    s = open(path, encoding="utf-8").read()
    assert s.count(old) == 1, "needle 在 %s 里出现 %d 次（期望 1）" % (path, s.count(old))
    open(path, "w", encoding="utf-8").write(s.replace(old, new))


def replace_in_func(path, func_sig, old, new):
    """只替换某个函数体内的第一个出现（★ 两个守卫的 needle 文本相同 ⇒ 必须按函数定位）。"""
    s = open(path, encoding="utf-8").read()
    fi = s.index(func_sig)
    j = s.index(old, fi)
    open(path, "w", encoding="utf-8").write(s[:j] + new + s[j + len(old):])


# ---------- M1：G-1 守卫被摘掉（判据③） ----------
def m1_mut():
    replace_in_func(CACHE, "func (c *SessionCache) loadFromDB(",
                    "if ShouldSkipLegacyLoad(ctx, gwSessionID) {",
                    "if len(gwSessionID) == 0 {")
def m1_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


# ---------- M2：G-2 守卫被摘掉（判据④） ----------
def m2_mut():
    replace_in_func(CACHE, "func (c *SessionCache) loadFromLegacyDB(",
                    "if ShouldSkipLegacyLoad(ctx, gwSessionID) {",
                    "if len(gwSessionID) == 0 {")
def m2_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


# ---------- M3：注入点传了【空 sessionID】（★ 语义上短路永不发生） ----------
def m3_mut():
    replace_once(HANDLER,
                 "ctx = compression.WithAutoCreatedSession(ctx, sessionID)",
                 "ctx = compression.WithAutoCreatedSession(ctx, \"\")")
def m3_restore():
    with open(HANDLER, "wb") as f:
        f.write(backups[HANDLER])


# ---------- M4：opt-in env 改名（判据②） ----------
def m4_mut():
    replace_once(CARRIER, "ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED",
                 "ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED_V2")
def m4_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


# ---------- M5：换回 kill-switch 型 API（★ ★★ 这正是历史上那一版错误实现） ----------
def m5_mut():
    s = open(CARRIER, encoding="utf-8").read()
    s = s.replace('import (\n\t"context"\n\t"os"',
                  'import (\n\t"context"\n\t"os"')
    s = s.replace('\t"sync"\n)', '\t"sync"\n\n\t"github.com/kaixuan/llm-gateway-go/settings"\n)')
    s = s.replace("if !skipAutoCreatedEnabled() {",
                  'if !settings.IsEnabled("session_cache_skip_auto_created") {')
    open(CARRIER, "w", encoding="utf-8").write(s)
def m5_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


# ---------- M6：删掉一条跳过日志（判据⑥） ----------
def m6_mut():
    replace_once(CACHE, "session_cache: skip legacy load, auto-created session",
                 "session_cache: skip legacy load")
def m6_restore():
    with open(CACHE, "wb") as f:
        f.write(backups[CACHE])


# ---------- M7：单测被删（判据⑦） ----------
def m7_mut():
    replace_once(UNIT, "func TestShouldSkipLegacyLoadRequiresEqualSessionID(",
                 "func TestShouldSkipLegacyLoadRequiresEqualSessionIDX(")
def m7_restore():
    with open(UNIT, "wb") as f:
        f.write(backups[UNIT])


# ---------- M8：文档登记被删（判据⑧） ----------
def m8_mut():
    s = open(RUNBOOK, encoding="utf-8").read()
    i = s.index("### §143.86")
    seg = s[i:]
    seg2 = seg.replace("ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED", "SKIP_AUTO_CREATED")
    assert seg2 != seg
    open(RUNBOOK, "w", encoding="utf-8").write(s[:i] + seg2)
def m8_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M9（★ 期望【仍然绿】）：★ kill-switch 写法只出现在【注释】里 ----------
def m9_mut():
    with open(CARRIER, "a", encoding="utf-8") as f:
        f.write("\n// 变异体：★ 注释里引用 settings.IsEnabled(\"session_cache_skip_auto_created\")\n"
                "// ★ 判据查的是【剥注释后的代码面】⇒ ⇒ 不应触发红\n")
def m9_restore():
    with open(CARRIER, "wb") as f:
        f.write(backups[CARRIER])


check("M1 G-1 守卫被摘掉（判据③）", True,
      "TestSection14386GuardG1InsideLoadFromDB", m1_mut, m1_restore)
check("M2 G-2 守卫被摘掉（判据④）", True,
      "TestSection14386GuardG2InsideLoadFromLegacyDB", m2_mut, m2_restore)
check("M3 注入点传空 sessionID（★ 语义上短路永不发生）", True,
      "TestSection14386InjectionIsPairedWithRequestContext", m3_mut, m3_restore)
check("M4 opt-in env 改名（判据②）", True,
      "TestSection14386SwitchIsOptInAndDefaultsOff", m4_mut, m4_restore)
check("M5 换回 kill-switch 型 API（★ 历史上那一版错误实现）（判据②）", True,
      "TestSection14386SwitchIsOptInAndDefaultsOff", m5_mut, m5_restore)
check("M6 删掉一条跳过日志（判据⑥）", True,
      "TestSection14386SkipLogsExist", m6_mut, m6_restore)
check("M7 单测被改名（判据⑦）", True,
      "TestSection14386UnitTestsExist", m7_mut, m7_restore)
check("M8 文档登记里的开关名被改（判据⑧）", True,
      "TestSection14386HandoffSurfacesExist", m8_mut, m8_restore)
check("M9 kill-switch 写法只出现在注释里（★ 期望【仍然绿】：★ 判据要剥注释）", False,
      "TestSection14386SwitchIsOptInAndDefaultsOff", m9_mut, m9_restore)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))
sys.exit(0 if n_ok == len(results) else 1)
