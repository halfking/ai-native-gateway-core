#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.85 门的变异台：★ 验证「设计门」也有牙 —— ★ 每个前提事实被改坏后必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14385_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 三条纪律（本脚本已内建）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须【编译干净】—— ★★ 【编译红 ≠ 断言红】★ ⇒ ⇒ 每个变异都挑【语义变了但仍能编译】的写法；
★   ③ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（★ M9）⇒ ⇒ 把边界【显式记录】而不是靠运气。
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

CACHE_LOCAL = os.path.join(ROOT, "domains/hooks/compression/session_cache.go")
CACHE_SHARED = os.path.join(ROOT, "domains/hooks/compression/session_cache_shared.go")
COMPRESSOR = os.path.join(ROOT, "domains/hooks/compression/session_compressor.go")
SESSION_V2 = os.path.join(ROOT, "domains/session/session_v2.go")
HANDLER = os.path.join(ROOT, "domains/streaming/handler.go")
MESSAGES = os.path.join(ROOT, "domains/streaming/messages.go")
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
MUTDIR = os.path.join(ROOT, "domains/hooks/compression/zz_mut14385_tmp")

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
    for p in (CACHE_LOCAL, CACHE_SHARED, COMPRESSOR, SESSION_V2, HANDLER, MESSAGES,
              LESSONS, AGENTS, RUNBOOK):
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
            print(out[-1500:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


def replace_once(path, old, new):
    s = open(path, encoding="utf-8").read()
    assert s.count(old) == 1, "needle 在 %s 里出现 %d 次（期望 1）" % (path, s.count(old))
    open(path, "w", encoding="utf-8").write(s.replace(old, new))


# ---------- M1：L-2 不再走共同汇合点（★ 编译干净：换成同签名的 loadFromRedis） ----------
def m1_mut():
    replace_once(CACHE_SHARED,
                 "_, body, err = c.loadFromLegacyDB(ctx, tenantID, sessionID)",
                 "_, body, err = c.loadFromRedis(ctx, tenantID, sessionID)")


def m1_restore():
    with open(CACHE_SHARED, "wb") as f:
        f.write(backups[CACHE_SHARED])


# ---------- M2：CreateV2 不再新生成 ID（★ 编译干净：generateSessionKey 同签名） ----------
def m2_mut():
    replace_once(SESSION_V2,
                 "sessionID := generateGwSessionID()",
                 "sessionID := generateSessionKey(apiKeyID, tenantID)")


def m2_restore():
    with open(SESSION_V2, "wb") as f:
        f.write(backups[SESSION_V2])


# ---------- M3：入口 1 不再传 ctx（★ 编译干净） ----------
def m3_mut():
    replace_once(COMPRESSOR,
                 "sc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)",
                 "sc.deps.Cache.GetOrLoad(context.Background(), tenantID, gwSessionID)")


def m3_restore():
    with open(COMPRESSOR, "wb") as f:
        f.write(backups[COMPRESSOR])


# ---------- M4：compression 包出现反向依赖文本（★ 编译干净：只放字符串常量，★ 不真 import） ----------
def m4_mut():
    os.makedirs(MUTDIR, exist_ok=True)
    with open(os.path.join(MUTDIR, "mut.go"), "w", encoding="utf-8") as f:
        f.write("package compression\n\n"
                "// ★ 变异体：★ 只放常量、★ 不真 import（★ 真 import 会造成循环依赖 ⇒ ⇒ 编译红 ⇒ ⇒ 测不了判据）\n"
                "var MutantReverseDependency = \"github.com/kaixuan/llm-gateway-go/domains/streaming\"\n")


def m4_restore():
    if os.path.isdir(MUTDIR):
        shutil.move(MUTDIR, tempfile.mkdtemp(prefix="mut14385_gone_"))


# ---------- M5：注入点挂错标志（★ 编译干净：AutoCreated / Resumed 同为 bool 字段） ----------
def m5_mut():
    replace_once(HANDLER, "if assignment.AutoCreated {", "if assignment.Resumed {")


def m5_restore():
    with open(HANDLER, "wb") as f:
        f.write(backups[HANDLER])


# ---------- M6：设计段少登记一个守卫（判据⑥） ----------
def m6_mut():
    s = open(RUNBOOK, encoding="utf-8").read()
    i = s.index("### §143.85")
    seg = s[i:]
    seg2 = seg.replace("G-2", "GX")
    assert seg2 != seg, "§143.85 段里没有 G-2"
    open(RUNBOOK, "w", encoding="utf-8").write(s[:i] + seg2)


def m6_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M7 / M8：交接面登记消失（判据⑦） ----------
def m7_mut():
    s = open(LESSONS, encoding="utf-8").read()
    s2 = s.replace("## §276", "## §2X6")
    assert s2 != s
    open(LESSONS, "w", encoding="utf-8").write(s2)


def m7_restore():
    with open(LESSONS, "wb") as f:
        f.write(backups[LESSONS])


def m8_mut():
    s = open(AGENTS, encoding="utf-8").read()
    s2 = s.replace("R128", "R1X8")
    assert s2 != s
    open(AGENTS, "w", encoding="utf-8").write(s2)


def m8_restore():
    with open(AGENTS, "wb") as f:
        f.write(backups[AGENTS])


# ---------- M9（★ 期望【仍然绿】）：★ 注释里出现 sessionCompressor ⇒ ⇒ 判据必须剥注释 ----------
def m9_mut():
    with open(MESSAGES, "a", encoding="utf-8") as f:
        f.write("\n// 变异体：★ 注释里提到 sessionCompressor（★ 判据剥注释 ⇒ ⇒ 不应触发红）\n")


def m9_restore():
    with open(MESSAGES, "wb") as f:
        f.write(backups[MESSAGES])


check("M1 L-2 不再走共同汇合点（判据①）", True,
      "TestSection14385LegacyLoadIsSharedChokePoint", m1_mut, m1_restore)
check("M2 CreateV2 不再新生成 sessionID（判据②）", True,
      "TestSection14385AutoCreatedImpliesFreshSessionID", m2_mut, m2_restore)
check("M3 压缩器入口不再传 ctx（判据③）", True,
      "TestSection14385AllTriggerEntriesPassContext", m3_mut, m3_restore)
check("M4 compression 出现反向依赖文本（判据④）", True,
      "TestSection14385CompressionMustNotImportStreaming", m4_mut, m4_restore)
check("M5 注入点挂到 Resumed 而不是 AutoCreated（判据⑤）", True,
      "TestSection14385InjectionPointIsAutoCreatedAndUnique", m5_mut, m5_restore)
check("M6 设计段少登记一个守卫 G-2（判据⑥）", True,
      "TestSection14385RunbookRegistersDesign", m6_mut, m6_restore)
check("M7 LESSONS 删掉「## §276」（判据⑦）", True,
      "TestSection14385HandoffSurfacesExist", m7_mut, m7_restore)
check("M8 AGENTS 删掉「R128」（判据⑦）", True,
      "TestSection14385HandoffSurfacesExist", m8_mut, m8_restore)
check("M9 注释里出现 sessionCompressor（★ 期望【仍然绿】：★ 判据必须剥注释）", False,
      "TestSection14385InjectionPointIsAutoCreatedAndUnique", m9_mut, m9_restore)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))
sys.exit(0 if n_ok == len(results) else 1)
