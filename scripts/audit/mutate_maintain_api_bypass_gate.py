#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「/maintain-api 匿名豁免」门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_maintain_api_bypass_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 纪律（★ 与仓内其它变异台同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（这里每个 needle 断言恰好 1 次）；
★   ③ 变异必须【语义变了但仍能编译】；
★   ④ ★ 判据有【边界】⇒ ⇒ 另设「期望仍然绿」的用例（M3/M4）——
★      M3 证明「行为腿有盲区、靠静态腿兜住」，M4 证明门不是「一改就红」的绊线；
★   ⑤ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）。

★ 背景：2026-10-23 实测缺陷 —— 全局 API-key 门（middleware/auth_mw.go）的
★   bypass 名单漏登记 canonical 运维 API 前缀 /maintain-api ⇒ 匿名探针
★   （/maintain-api/healthz、/maintain-api/menu/ops）被抢先拦成 **401 missing_key**，
★   而不是 maintain_proxy.go 设计的 **503 maintain.not_configured** ⇒
★   前端分不清「运维平台没配」与「真维护态」。
"""
import hashlib
import os
import subprocess
import sys

# ★ Windows（cp936）上 stdout 打不出 emoji ⇒ UnicodeEncodeError 会**中断变异台**，
#   而中断点在「已变异、未还原」的窗口里 ⇒ 最坏结果是把变异留在工作树里。
#   ⇒ 输出统一 UTF-8 且 errors="replace"；打印一律走 emit()（永不抛）。
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:  # pragma: no cover - 极老 Python
    pass


def emit(msg):
    try:
        print(msg)
    except Exception:  # pragma: no cover - 输出失败不得中断流程
        try:
            print(msg.encode("ascii", "replace").decode("ascii"))
        except Exception:
            pass


ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

AUTH_MW = os.path.join(ROOT, "middleware", "auth_mw.go")
MW_GATE = os.path.join(ROOT, "middleware", "maintain_api_bypass_test.go")
GW_GATE = os.path.join(ROOT, "cmd", "gateway", "maintain_api_bypass_contract_test.go")

WATCHED = (AUTH_MW, MW_GATE, GW_GATE)

STATIC_PATTERN = "TestMaintainAPI"
BEHAVIOR_PATTERN = "TestMaintainAPI"

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
        emit("❌ 还原校验失败：" + repr(bad))
        sys.exit(1)
    emit("✅ 还原校验：%d 个文件 md5 逐字一致" % len(backups))


def git_status():
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    return p.stdout


def file_md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def env_fingerprint():
    return {"status": git_status(), "md5": {p: file_md5(p) for p in WATCHED}}


ENV_BEFORE = env_fingerprint()


def run_gate(pkg, pattern):
    env = dict(os.environ)
    env.pop("TEST_DATABASE_URL", None)
    p = subprocess.run(
        ["go", "test", pkg, "-run", pattern, "-count=1"],
        cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace", env=env,
        timeout=1800,
    )
    return p.returncode != 0, (p.stdout + p.stderr)


results = []


def check(name, expect_red, target, pattern, mutate, restore):
    for p in WATCHED:
        snap(p)
    try:
        mutate()
        red, out = run_gate(target, pattern)
        ok = (red == expect_red)
        mark = "🔴" if red else "🟢"
        want = "变红 ✔" if expect_red else "仍然绿 ✔"
        got = "变红" if red else "仍然绿"
        emit(f"{mark} {name} ⇒ 期望{want} / 实得{got}" + ("" if ok else "  ❌"))
        if not ok:
            emit(out[-2000:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, s):
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write(s)


def replace_once(path, old, new):
    s = read(path)
    assert s.count(old) == 1, "needle 在 %s 里出现 %d 次（期望 1）：%r" % (path, s.count(old), old)
    write(path, s.replace(old, new))


# ---------- M1：★ ExactPaths 里的 "/maintain-api" 被摘掉 ----------
def m1_mut():
    replace_once(AUTH_MW, '"/", "/index.html", "/m", "/maintain", "/maintain-api",',
                 '"/", "/index.html", "/m", "/maintain",')
def m1_restore():
    with open(AUTH_MW, "wb") as f:
        f.write(backups[AUTH_MW])


# ---------- M2：★ PathPrefixes 里的 "/maintain-api/" 被摘掉 ----------
def m2_mut():
    replace_once(AUTH_MW, '"/maintain/", "/maintain-api/", "/plugins/"',
                 '"/maintain/", "/plugins/"')
def m2_restore():
    with open(AUTH_MW, "wb") as f:
        f.write(backups[AUTH_MW])


# ---------- M3：★ 前缀被写成裸 "/maintain-api"（少尾斜杠） ----------
# ★ 行为腿**期望仍然绿**（裸前缀仍能匹配 /maintain-api/...），
#   而静态「不得越界」腿必须变红 ⇒ ★ 这正是静态腿存在的理由：
#   行为腿有盲区（看不出「顺带放开了 /maintain-apixxx」）。
def m3_mut():
    replace_once(AUTH_MW, '"/maintain/", "/maintain-api/", "/plugins/"',
                 '"/maintain/", "/maintain-api", "/plugins/"')
def m3_restore():
    with open(AUTH_MW, "wb") as f:
        f.write(backups[AUTH_MW])


# ---------- M4：★ 无语义改动的注释行（★ 期望两门都仍然绿） ----------
def m4_mut():
    replace_once(AUTH_MW,
                 "\t\t\tPathPrefixes: []string{",
                 "\t\t\t// mutate_gate: 无语义改动，仅供变异台对照\n\t\t\tPathPrefixes: []string{")
def m4_restore():
    with open(AUTH_MW, "wb") as f:
        f.write(backups[AUTH_MW])


def main():
    # M1 / M2：两条腿都该变红
    check("M1 ExactPaths 摘掉 /maintain-api（静态腿）", True, "./middleware/", STATIC_PATTERN, m1_mut, m1_restore)
    check("M1 ExactPaths 摘掉 /maintain-api（行为腿）", True, "./cmd/gateway/", BEHAVIOR_PATTERN, m1_mut, m1_restore)
    check("M2 PathPrefixes 摘掉 /maintain-api/（静态腿）", True, "./middleware/", STATIC_PATTERN, m2_mut, m2_restore)
    check("M2 PathPrefixes 摘掉 /maintain-api/（行为腿）", True, "./cmd/gateway/", BEHAVIOR_PATTERN, m2_mut, m2_restore)
    # M3：行为腿仍绿，静态腿必须红
    check("M3 前缀少尾斜杠（行为腿，★ 期望仍然绿）", False, "./cmd/gateway/", BEHAVIOR_PATTERN, m3_mut, m3_restore)
    check("M3 前缀少尾斜杠（静态越界腿，★ 期望变红）", True, "./middleware/", STATIC_PATTERN, m3_mut, m3_restore)
    # M4：无语义改动 ⇒ 两门都仍然绿
    check("M4 无语义改动（静态腿，★ 期望仍然绿）", False, "./middleware/", STATIC_PATTERN, m4_mut, m4_restore)
    check("M4 无语义改动（行为腿，★ 期望仍然绿）", False, "./cmd/gateway/", BEHAVIOR_PATTERN, m4_mut, m4_restore)

    # 环境指纹对拍：变异期间除被改文件外不应有别的变化
    env_after = env_fingerprint()
    changed = [p for p in WATCHED if env_after["md5"][p] != ENV_BEFORE["md5"][p]]
    if changed:
        emit("❌ 还原后指纹不一致：", changed)
        results.append(("env fingerprint", False))
    else:
        emit("✅ 环境指纹对拍：3 个被观测文件 md5 逐字一致")
        results.append(("env fingerprint", True))

    failed = [n for n, ok in results if not ok]
    emit("")
    if failed:
        emit("❌ 变异台未达预期：", failed)
        return 1
    emit("✅ 变异台全部符合预期（%d 项）" % len(results))
    return 0


if __name__ == "__main__":
    sys.exit(main())
