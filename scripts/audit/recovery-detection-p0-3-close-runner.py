#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""★★ P0-3 **关窗跑批器**（★ Stage 1 / Stage 2）

★★ **为什么需要它（★ 本轮真踩到的三类风险）**：
★★ ① ★ 七步链**有顺序依赖**（★ 预演 → 审计 → 自测 → 负控自检 → 时钟门 → 真判 → 负控）
★★   ⇒ ★★ 手工跑容易**漏步 / 错序** ⇒ ★★ 关窗当天时间紧 ⇒ ★★ 一次漏步就可能**整表作废**
★★ ② ★★ **rc=0 不等于读数对**（★ 例：「守卫缺失」也会 rc=0 ⇒ ★ 必须校验输出里的**期望值**）
★★   ⇒ ★★ 本 runner 对每一步都校验 **必须出现的标记** + **必须不出现的标记** + **最小计数**
★★ ③ ★★ **窗口没满也能跑出「看起来正常」的读数**
★★   （★ 第 26 处实测：★ 同一批数据，★ 真实 now ❌ / 预演 ✅ ⇒ ★ **裁决符号会摇摆**）
★★   ⇒ ★★ 必须先过「**窗口已满**」这道门（★ 用**数据库的 `now()`**，★ **不用本机时钟**）

★★★ **纪律（★ 本项目的硬约束）**：
★★★ ★ ★ **任一违例 ⇒ rc ≠ 0**（★ 与「任何作废/报警都必须同时改退出码」同源）
★★★ ★ ★ **不得手写诚实边界**（★ 清单从判定器输出里**摘**，★ 那是唯一来源）
★★★ ★ ★ **容器名不得写死**（★ 自动发现 ⇒ 取不到 / 不唯一 ⇒ **显式失败**）

用法：
    python3 scripts/audit/recovery-detection-p0-3-close-runner.py --stage 1
    python3 scripts/audit/recovery-detection-p0-3-close-runner.py --stage 2
    python3 scripts/audit/recovery-detection-p0-3-close-runner.py --stage 1 --dry-run
"""

from __future__ import annotations

import argparse
import datetime
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

AUD = Path(__file__).resolve().parent

# ★★ era 切点（★ 观测窗口的起点，★ 主档 §5.9.21.13.x 已多次核查机制连续性 ⇒ 有效）
EPOCH = "2026-11-10 13:42:30+08"
TZ8 = datetime.timezone(datetime.timedelta(hours=8))
EPOCH_TS = datetime.datetime(2026, 11, 10, 13, 42, 30, tzinfo=TZ8)

# ★ Stage 窗口长度（★ Stage 1 = 24h、★ Stage 2 = 168h）
STAGE_HOURS = {1: 24, 2: 168}

# ★★ 诚实边界的圈号集合（★ 用于从判定器输出里**摘**清单，★ 不得手写）
CIRCLED = set(
    "①②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳"
    "㉑㉒㉓㉔㉕㉖㉗㉘㉙㉚㉛㉜㉝㉞㉟㉠㉡㉢㉣㉤㉥㉦㉧㉨㉩㉪㉫㉬㉭㉮㉯㉰㉱㉲㉳㉴"
)

VERDICT_SYM = ("✅", "❌", "⚠️")


# ─────────────────────────────────────────────────────────────────────
# 环境事实：容器名**自动发现**（★ 不得写死）
# ─────────────────────────────────────────────────────────────────────
def resolve_pg_container(host: str) -> str:
    """★ 自动发现 211 上的 PostgreSQL 容器名。

    ★★ **为什么不能写死**（★ 本项目的硬约束）：★ 环境事实会漂（★ 容器重建 / 改名）
    ★★ ⇒ ★★ 写死 ⇒ ★★ 静默取到别的容器 / 直接失败 ⇒ ★★ **静默失效**
    ★★ ⇒ ★★ 优先用**约定名**，★ 取不到则按**镜像/名称特征**找，★ **不唯一 ⇒ 显式失败**。
    """
    out = subprocess.run(
        ["ssh", host, "docker ps --format '{{.Names}}'"],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        raise SystemExit(f"★ 无法列出 {host} 上的容器 ⇒ {out.stderr.strip()}")
    names = [n.strip() for n in out.stdout.splitlines() if n.strip()]
    if not names:
        raise SystemExit(f"★ {host} 上没有运行中的容器 ⇒ ★ **显式失败**（★ 不是「读数缺失」）")
    if "acc-pg17" in names:
        return "acc-pg17"
    cand = [n for n in names if "pg" in n.lower() or "postgres" in n.lower()]
    if len(cand) == 1:
        return cand[0]
    raise SystemExit(
        f"★ {host} 上的 PostgreSQL 容器**不唯一或找不到** ⇒ 候选 = {cand or '（无）'}；"
        f"全部容器 = {names} ⇒ ★ **显式失败**（★ 不猜）"
    )


def db_now(host: str, container: str) -> datetime.datetime:
    """★ 取**数据库的** `now()`（★ 窗口是否已满以它为准，★ 不用本机时钟）。"""
    cmd = (
        f"docker exec {container} psql -U acc_super -d llm_gateway -tA "
        f"-v ON_ERROR_STOP=1 -c \"SELECT to_char(now() AT TIME ZONE 'Asia/Shanghai', "
        f"'YYYY-MM-DD HH24:MI:SS');\""
    )
    out = subprocess.run(["ssh", host, cmd], capture_output=True, text=True)
    if out.returncode != 0:
        raise SystemExit(f"★ 取数据库 now() 失败 ⇒ {out.stderr.strip()}")
    raw = out.stdout.strip().splitlines()
    if not raw:
        raise SystemExit("★ 数据库 now() 没有输出 ⇒ ★ **显式失败**")
    return datetime.datetime.strptime(raw[0].strip(), "%Y-%m-%d %H:%M:%S").replace(tzinfo=TZ8)


# ─────────────────────────────────────────────────────────────────────
# 步骤定义
# ─────────────────────────────────────────────────────────────────────
class Step:
    def __init__(self, name: str, cmd: list[str], *,
                 must: list[str] | None = None,
                 must_not: list[str] | None = None,
                 min_count: dict[str, int] | None = None,
                 note: str = ""):
        self.name = name
        self.cmd = cmd
        self.must = must or []
        self.must_not = must_not or []
        self.min_count = min_count or {}
        self.note = note


def build_steps(stage: int) -> list[Step]:
    py = sys.executable or "python3"
    v = "recovery-detection-p0-3-verdict.py"
    a = "recovery-detection-p0-3-post-bound-audit.py"
    n = "recovery-detection-p0-3-negcontrol.py"
    c = "recovery-detection-p0-3-clockcheck.py"
    p = "recovery-detection-p0-3-stage1-projection.py"
    steps = [
        Step("① 关窗预演（as-of = 关窗时钟）",
             [py, str(AUD / p), "--stage", str(stage)],
             must=["关窗预演通过"],
             note="★ 预演只投影**时钟**，★ 不投影数据积累 ⇒ ★ 一切以关窗真读为准"),
        Step("② 穷举审计 v2（post 段必须有 now() 上界）",
             [py, str(AUD / a)],
             must=["覆盖率守卫：★ 9 个关键块全部可见",
                   "全部 post 段 / 事件窗口型读数都具备有效的上界"],
             note="★ v2：★ 38 块全可见 + 覆盖率守卫 ⇒ ★ 「全部合规」必须附带覆盖率"),
        Step("③ 判定器自测",
             [py, str(AUD / v), "--selftest"],
             must=["合成数据自测通过"],
             min_count={"✔ ": 47},
             note="★ **47 条断言项**（★ 含 ㉮㉯㉱㉳㉴）"),
        Step("④ 负控几何离线自检",
             [py, str(AUD / n), "--selftest"],
             must=["负控几何自检通过"],
             note="★ 5 项：★ `now()` 随切点平移 + Stage 1 必须判负清单含 M11"),
        Step("⑤ 三时钟门（pre 基线时钟无关）",
             [py, str(AUD / c)],
             must=["三时钟检查通过"],
             note="★ 44 pre 键 + 1 披露键逐字节一致 ⇒ ★ pre 基线未漂"),
        Step("⑥ 真实判定",
             [py, str(AUD / v), "--stage", str(stage)],
             must=["M20 fp 视图尾部可见性: ✔"],
             must_not=["★★ **量具守卫未通过"],
             note="★ 守卫全绿 ⇒ ★ 裁决表可用"),
    ]
    if stage == 1:
        steps.append(Step("⑦ 负控 Stage 1（era 前移 1 天）",
                          [py, str(AUD / n), "--shift-days", "1", "--stage", "1"],
                          must=["负控通过"],
                          note="★ 效应判据必须判负 + M11 ns 必须判负 ⇒ ★ 门有牙"))
    else:
        steps.append(Step("⑦ 负控 Stage 2（era 前移 7 天）",
                          [py, str(AUD / n), "--shift-days", "7", "--stage", "2"],
                          must=["负控通过"],
                          note="★ 效应判据必须判负（★ M11 ns 判 ✅ 是**正确**的）"))
    return steps


# ─────────────────────────────────────────────────────────────────────
# 裁决卡解析
# ─────────────────────────────────────────────────────────────────────
def parse_verdict_rows(text: str) -> list[tuple[str, str, str]]:
    """★ 从裁决表里抽出 (判据名, 符号, 裁决文案)。

    ★★ **为什么不能按 `|` 切**（★ 真坑）：★ 观测列**本身就含 `|`**
    （★ 例：`【指纹·fp】n_all=29 | top21600_n=0 | ...`）⇒ ★★ 按 `|` 切会串列
    ★★ ⇒ ★★ 改用「★ **最后一个**以裁决符号开头的片段」。
    """
    rows: list[tuple[str, str, str]] = []
    for line in text.splitlines():
        if not line or line.startswith("==") or line.startswith("---"):
            continue
        if line.startswith("判据") or "|" not in line:
            continue
        parts = line.split("|")
        idx = None
        sym = None
        for i in range(len(parts) - 1, -1, -1):
            st = parts[i].strip()
            for cand in VERDICT_SYM:
                if st.startswith(cand):
                    idx = i
                    sym = cand
                    break
            if idx is not None:
                break
        if idx is None or idx == 0 or sym is None:
            continue
        label = parts[0].strip()
        verdict = "|".join(parts[idx:]).strip()
        rows.append((label, sym, verdict))
    return rows


def extract_honest_boundaries(text: str) -> list[str]:
    """★ **摘**诚实边界清单（★ 唯一来源 = 判定器打印的那份 ⇒ 不得手写）。"""
    out = []
    for line in text.splitlines():
        if len(line) > 3 and line[:3] == "   " and line[3] in CIRCLED:
            out.append(line.strip())
    return out


# ★★ 裁决文案里的**披露标记** ⇒ 用于裁决卡上给每条 ❌ 挂上「该怎么读」
DISCLOSURE_FLAGS = [
    ("不可归因于修复", "★ 不可归因于修复（㉘：阈值取自 pre24 单日快照）"),
    ("取整边界伪影", "★ 取整边界伪影（㉭：真实差上界 ≈ 60 秒 ⇒ 不是行为回归）"),
    ("两个 n 不同窗口", "★ 两个 n 不同窗口（第 28 处 ⇒ 不得读成「尾部有回归」）"),
    ("小样本分位数不稳", "★ 小样本分位数不稳（㉰ ⇒ 须与关窗当天真读比）"),
    ("量纲不同", "★ 基线量纲不同（㉚ ⇒ 当描述性读数）"),
    ("未被证伪", "★ 未被负控证伪（㉱ ⇒ 只写「未被证伪」）"),
]


def flags_for(verdict: str) -> list[str]:
    return [txt for key, txt in DISCLOSURE_FLAGS if key in verdict]


# ─────────────────────────────────────────────────────────────────────
# 主流程
# ─────────────────────────────────────────────────────────────────────
def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--stage", type=int, choices=[1, 2], default=1)
    ap.add_argument("--host", default="211")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--log-dir", default="")
    ap.add_argument("--force-unmatured-window", action="store_true",
                    help="★ 关窗前**链路自检**用：★ 跳过「窗口已满」这道门（★ 关窗裁决**禁用**）")
    args = ap.parse_args()

    stage = args.stage
    steps = build_steps(stage)
    close_ts = EPOCH_TS + datetime.timedelta(hours=STAGE_HOURS[stage])

    print("=" * 96)
    print(f"★★ P0-3 关窗跑批器（★ Stage {stage}）")
    print(f"★★ era 切点 = {EPOCH} ｜ ★ 关窗 = {close_ts:%Y-%m-%d %H:%M:%S} +08"
          f" ｜ ★ 本机 = {datetime.datetime.now(TZ8):%Y-%m-%d %H:%M:%S} +08")
    print("=" * 96)

    if args.dry_run:
        for s in steps:
            print(f"  · {s.name}")
            print(f"      $ {' '.join(s.cmd)}")
            if s.must:
                print(f"      ★ 必须出现: {s.must}")
            if s.must_not:
                print(f"      ★ 必须不出现: {s.must_not}")
            if s.min_count:
                print(f"      ★ 最小计数: {s.min_count}")
        print(f"★★ 关窗 = {close_ts:%Y-%m-%d %H:%M:%S} +08")
        return 0

    # ── 窗口门（★ 用**数据库的 now()**）────────────────────────────────
    try:
        container = resolve_pg_container(args.host)
    except SystemExit as e:
        print(f"★★ ❌ 环境门失败：{e}")
        return 3
    try:
        dnow = db_now(args.host, container)
    except SystemExit as e:
        print(f"★★ ❌ 时钟门失败：{e}")
        return 3
    remain = (close_ts - dnow).total_seconds()
    print(f"★ 容器 = {container} ｜ ★ 数据库 now() = {dnow:%Y-%m-%d %H:%M:%S} +08")
    if remain > 0:
        if args.force_unmatured_window:
            print(f"★★ ⚠️⚠️⚠️ **已跳过「窗口已满」这道门**（★ 距关窗还有 {remain/3600:.2f} 小时）")
            print("★★ ⚠️⚠️⚠️ ★ 本次结果**只是链路自检**，★ **不可作关窗裁决**"
                  "（★ 第 26 处实测：★ 同一批数据，★ 真实 now ❌ / 预演 ✅ ⇒ ★ 裁决符号会摇摆）")
        else:
            print(f"★★ ❌ **窗口未满** ⇒ 距关窗还有 {remain/3600:.2f} 小时"
                  f"（★ 关窗 = {close_ts:%Y-%m-%d %H:%M:%S} +08）")
            print("★★ ⇒ ★ 窗口未满时的读数 ≠ 关窗读数（★ 第 26 处实测：★ 同一批数据，"
                  "真实 now ❌ / 预演 ✅）⇒ ★★ **先别出结论**")
            return 4
    else:
        print(f"★★ ✔ 窗口已满（★ 超关窗时刻 {-remain/3600:.2f} 小时）")

    # ── 日志目录 ────────────────────────────────────────────────────────
    ts = datetime.datetime.now(TZ8).strftime("%Y%m%d-%H%M%S")
    log_dir = Path(args.log_dir) if args.log_dir else Path(tempfile.gettempdir()) / f"p0-3-close-s{stage}-{ts}"
    log_dir.mkdir(parents=True, exist_ok=True)

    failures: list[str] = []
    verdict_text = ""
    for i, s in enumerate(steps, 1):
        print(f"\n───── [{i}/{len(steps)}] {s.name}")
        print(f"      $ {' '.join(s.cmd)}")
        proc = subprocess.run(s.cmd, capture_output=True, text=True)
        out = proc.stdout or ""
        err = proc.stderr or ""
        logf = log_dir / f"{i:02d}.log"
        logf.write_text(out + ("\n--- STDERR ---\n" + err if err else ""), encoding="utf-8")
        bad: list[str] = []
        if proc.returncode != 0:
            bad.append(f"rc={proc.returncode}（★ 期望 0）")
        for m in s.must:
            if m not in out:
                bad.append(f"★ 缺少必须出现的标记：{m!r}")
        for m in s.must_not:
            if m in out:
                bad.append(f"★ 出现了必须不出现的标记：{m!r}")
        for m, k in s.min_count.items():
            got = out.count(m)
            if got < k:
                bad.append(f"★ 标记 {m!r} 只出现 {got} 次（★ 期望 ≥ {k}）")
        if bad:
            failures.append(f"[{s.name}] " + "；".join(bad))
            print(f"      ✘ {s.name} ⇒ " + "；".join(bad))
            print(f"      ★ 完整输出见 {logf}")
        else:
            print(f"      ✔ {s.name}（★ {s.note}）")
        if s.cmd[1].endswith("recovery-detection-p0-3-verdict.py") and "--stage" in s.cmd:
            verdict_text = out

    # ── 裁决卡 ──────────────────────────────────────────────────────────
    rows = parse_verdict_rows(verdict_text)
    n_ok = sum(1 for _, s, _ in rows if s == "✅")
    n_bad = sum(1 for _, s, _ in rows if s == "❌")
    n_unk = sum(1 for _, s, _ in rows if s == "⚠️")
    boundaries = extract_honest_boundaries(verdict_text)

    print("\n" + "=" * 96)
    print(f"★★ 关窗裁决卡（Stage {stage}）｜ ★ 数据库 now() = {dnow:%Y-%m-%d %H:%M:%S} +08")
    print(f"★★ 符号统计：✅ {n_ok} ｜ ❌ {n_bad} ｜ ⚠️ {n_unk}（★ 共 {len(rows)} 行）")
    if n_bad:
        print("★★ ❌ 明细（★ **符号保持原样** ⇒ 门不钝化 ⇒ ★ 披露只解释、不软化）：")
        for label, sym, v in rows:
            if sym != "❌":
                continue
            print(f"   ❌ {label}")
            head = v.split("—", 1)[-1].strip()[:160] if "—" in v else v[:160]
            print(f"      {head}")
            for fl in flags_for(v):
                print(f"      {fl}")
    if n_unk:
        print(f"★★ ⚠️ {n_unk} 行「样本不足/不可判」（★ **不得**外推成 ✅ 或 ❌）")
    print(f"★★ 诚实边界清单（★ 从判定器输出**摘** ⇒ 不得手写）：{len(boundaries)} 条")
    print("   " + " ".join(b[0] if b else "?" for b in boundaries))
    print(f"★★ 完整日志：{log_dir}")

    if failures:
        print("\n" + "=" * 96)
        print("★★ ❌ 关窗跑批失败（★ 任一违例 ⇒ rc ≠ 0）：")
        for f in failures:
            print(f"   · {f}")
        return 1
    print("\n★★ ✔ 关窗跑批全部通过（★ 七步链 + 期望值校验全绿）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
