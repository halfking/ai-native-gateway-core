#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.84 的变异台：★ 验证「门绿 ≠ 门有牙」⇒ ⇒ 每个判据改坏后必须变红。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14384_gate.py

★ 退出码：0 = 全部按预期；非 0 = 有用例的预期没达成（★ 门可能没牙，或断言过期）。

★ ★★ 三条纪律（本脚本已内建）：
★   ① 还原必须按【原路径】做 md5 逐字校验（★ 否则「还原校验读错路径」会给假读数）；
★   ② 变异必须落在【判据实际读到的那一处】（★ 例：M9 只加【定义行】⇒ ⇒ 判据【刻意】不看 ⇒ ⇒ 期望【仍然绿】）；
★   ③ ★★ 每个变异都跑【指定的用例】⇒ ⇒ 【编译红 ≠ 断言红】★ ⇒ ⇒ 变异文件一律放在
★      ★ 独立的包目录里（★ 不参与 ./cmd/gateway/ 的编译）⇒ ⇒ 避免编译错误把断言结果盖掉。
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

# ★ ROOT 由脚本位置推导（★ 不写死绝对路径 ⇒ ⇒ 换机器也能跑）
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

SQL = os.path.join(ROOT, "scripts/audit/verify_l3_leg_value.sql")
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
MUTDIR = os.path.join(ROOT, "tmp_mut14384")

backups = {}


def snap(path):
    if path not in backups:
        with open(path, "rb") as f:
            backups[path] = f.read()


def restore_all():
    """还原 + 按【原路径】做 md5 逐字校验。"""
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
    """expect_red=True ⇒ 必须变红（判据有牙）；False ⇒ 必须仍然绿（★ 显式记录判据边界）。"""
    for p in (SQL, LESSONS, AGENTS, RUNBOOK):
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
            print(out[-1200:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


# ---------- M1：needle 双括号 → 单括号（判据⑥） ----------
def m1_mut():
    s = open(SQL, encoding="utf-8").read()
    s = s.replace("AND ((sb.outbound_body IS NOT NULL AND sb.outbound_body <>%",
                  "AND (sb.outbound_body IS NOT NULL AND sb.outbound_body <>%")
    open(SQL, "w", encoding="utf-8").write(s)


def m1_restore():
    with open(SQL, "wb") as f:
        f.write(backups[SQL])


# ---------- M2 / M7 / M8 / M9：在独立包目录里放变异体（★ 不参与 ./cmd/gateway/ 编译） ----------
def _write_mut(body):
    os.makedirs(MUTDIR, exist_ok=True)
    with open(os.path.join(MUTDIR, "mut.go"), "w", encoding="utf-8") as f:
        f.write(body)


def _drop_mut():
    # ★ 本地 rm 可能被运行时拦 ⇒ 用 mv 移出仓库
    if os.path.isdir(MUTDIR):
        shutil.move(MUTDIR, tempfile.mkdtemp(prefix="mut14384_gone_"))


def m2_mut():
    _write_mut('package tmpmut14384\n\nvar MutantSQL = "SELECT ff.outbound_body FROM x"\n')


def m7_mut():
    _write_mut(
        "package tmpmut14384\n\ntype pgBackendAdapter struct{}\n\n"
        "func (a *pgBackendAdapter) LastOutboundForSession(x string) ([]byte, error) { return nil, nil }\n"
    )


def m8_mut():
    # ★★★ 判据③ 的盲区样本：★ 注入点【藏在单行 func 体内】⇒ ⇒ 旧写法看不见
    _write_mut(
        "package tmpmut14384\n\nfunc dbBackendFromPool(x int) any { return nil }\n\n"
        "func init() { _ = dbBackendFromPool(1) }\n"
    )


def m9_mut():
    # ★ 只加【定义行】⇒ ⇒ 判据【刻意】只看【注入点】⇒ ⇒ 期望【仍然绿】
    _write_mut("package tmpmut14384\n\nfunc dbBackendFromPool(x int) any { return nil }\n")


# ---------- M3：runbook §143.84 段删掉「26.2」（判据⑧） ----------
def m3_mut():
    s = open(RUNBOOK, encoding="utf-8").read()
    i = s.index("### §143.84")
    seg = s[i:]
    seg2 = seg.replace("26.2", "27.7")
    assert seg2 != seg, "未替换到 26.2"
    open(RUNBOOK, "w", encoding="utf-8").write(s[:i] + seg2)


def m3_restore():
    with open(RUNBOOK, "wb") as f:
        f.write(backups[RUNBOOK])


# ---------- M4 / M5：交接面登记消失（判据⑨） ----------
def m4_mut():
    s = open(LESSONS, encoding="utf-8").read()
    s2 = s.replace("## §275", "## §2X5")
    assert s2 != s
    open(LESSONS, "w", encoding="utf-8").write(s2)


def m4_restore():
    with open(LESSONS, "wb") as f:
        f.write(backups[LESSONS])


def m5_mut():
    s = open(AGENTS, encoding="utf-8").read()
    s2 = s.replace("R127", "R1X7")
    assert s2 != s
    open(AGENTS, "w", encoding="utf-8").write(s2)


def m5_restore():
    with open(AGENTS, "wb") as f:
        f.write(backups[AGENTS])


# ---------- M6：脚本删掉「≥10 分钟」（判据⑦） ----------
def m6_mut():
    s = open(SQL, encoding="utf-8").read()
    s2 = s.replace("≥10 分钟", "10 分钟以上")
    assert s2 != s
    open(SQL, "w", encoding="utf-8").write(s2)


def m6_restore():
    with open(SQL, "wb") as f:
        f.write(backups[SQL])


check("M1 needle 双括号→单括号（判据⑥）", True,
      "TestSection14384LegValueNeedleMatchesRealShape", m1_mut, m1_restore)
check("M2 新增第二个 L3 SQL 文本生产者（判据①）", True,
      "TestSection14384L3SQLTextHasSingleProducer", m2_mut, _drop_mut)
check("M3 runbook §143.84 删掉「26.2」（判据⑧）", True,
      "TestSection14384RunbookRegistersDecompositionAndToolDefects", m3_mut, m3_restore)
check("M4 LESSONS 删掉「## §275」（判据⑨）", True,
      "TestSection14384HandoffSurfacesExist", m4_mut, m4_restore)
check("M5 AGENTS 删掉「R127」（判据⑨）", True,
      "TestSection14384HandoffSurfacesExist", m5_mut, m5_restore)
check("M6 脚本删掉「≥10 分钟」（判据⑦）", True,
      "TestSection14384LegValueScriptWarnsAboutShortWindow", m6_mut, m6_restore)
check("M7 新增第二个 PG 实现（判据②）", True,
      "TestSection14384LastOutboundHasSinglePGImplementation", m7_mut, _drop_mut)
check("M8 新增第二处 PG 后端注入（★ 藏在单行 func 体内）（判据③）", True,
      "TestSection14384PGBackendInjectedExactlyOnce", m8_mut, _drop_mut)
check("M9 只新增【定义行】（★ 判据刻意只看【注入点】⇒ 期望仍然绿）", False,
      "TestSection14384PGBackendInjectedExactlyOnce", m9_mut, _drop_mut)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))
sys.exit(0 if n_ok == len(results) else 1)
