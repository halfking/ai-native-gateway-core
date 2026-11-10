#!/usr/bin/env python3
"""recovery-detection-p0-3-verdict.py —— Phase 2b 观测层判定器（可复采）

★ 用途：era2（2026-11-10 13:42:30 +08）24h / 7 天窗口关闭后，**一条命令**跑完
  复采 + 对拍 + 判定，输出逐条判据的裁决表。

★ 为什么要有它：判据与基线**在窗口关闭前就写死**（见
  docs/audit/2026-11-10-P0-3验收判据审计.md §3）⇒ ★ 关窗时不该再临时解释口径。
  ⇒ ★ 裁决表把「可判 / 不可判 / 样本不足」也一并输出 ⇒ 避免「关窗才发现判据用不了」。

★ 执行（本地）：
    python3 scripts/audit/recovery-detection-p0-3-verdict.py            # 默认 Stage 1
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --stage 2
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --ssh-host 211

★ 依赖：ssh/scp 可用；211 上的容器 acc-pg17 与在役网关容器可达。
  ★ 唯一权威库连接来自在役容器 env（不凭记忆写连接串）。
"""
from __future__ import annotations

import argparse
import re
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).with_name("recovery-detection-p0-3-metrics.sql")

# ★★ 基线（全部来自 T1 锚点，2026-11-10 13:45~14:05 +08 实测；★ 写「方法+观测值」）
#    —— ★ 样本量规则同样预先写死：post episode < 5 ⇒ 「样本不足以判定」，不许外推。
BASELINES = {
    # 判据 → (基线值, 期望, 说明)
    "M2_static": (210, "==210", "featured 集合规模（era2 T0）"),
    "M6_free_featured_pairs": (80, ">=80", "免费在服对 featured 命中（★ 资格面，非行为面）"),
    "M9_missing_raw_variant": (0, "==0", "展开覆盖完整性"),
    "M5_probe_runs_24h": (9242, "<=9334", "24h 探测量（爆炸半径；9233×1.01）"),
    # ★★ 以下基线是**修正 join 后**的实测值（pre 段 7 天，2026-11-10 14:2x +08）
    "M11_ns_pre": ((3, 360, 1791), "<=", "namespaced 恢复感知时延 pre（med/p90/max 分钟）"),
    "M11_bare_pre": ((3, 180, 360), "<=", "bare 恢复感知时延 pre（med/p90/max 分钟）"),
    "M12s_ns_feat_pre": ((3, 60, 1791), "<=", "ns featured 复检间隔 pre（med/p90/p99 分钟）"),
    "M12s_ns_not_pre": ((360, 361, 5524), "-", "ns 非 featured 复检间隔 pre（★ 对照，不应变快）"),
    "M12p_pending_ns_not_pre": (1.3, "<=", "ns 非 featured 待复检率 pre %（★ 护栏）"),
}

# ★ 三个「冷金丝雀」：目标人群（106 个新覆盖变体）里仍被探测到的冷对
CANARIES = [
    "minimaxai/minimax-m2.7",
    "minimaxai/minimax-m3",
    "deepseek-ai/deepseek-v4-pro-0813",
    "z-ai/glm-5.3",          # ★ 修正 join 后才发现它（pre 13 events / med 360 min）
]
CANARY_PRE_MED = 360          # ★ pre 段实测中位（各 13 次/7 天）
CANARY_POST_MAX_MED = 180     # ★ 期望：featured backoff 减半 ⇒ ≤180 分钟

EPOCH = "2026-11-10 13:42:30+08"


def run_metrics(host: str) -> str:
    """把验收脚本送到 211 → 容器 → psql 执行，返回原始输出。"""
    with tempfile.TemporaryDirectory() as td:
        local = Path(td) / "m.sql"
        local.write_text(SCRIPT.read_text(encoding="utf-8"))
        subprocess.run(["scp", "-q", str(local), f"{host}:/tmp/m.sql"], check=True)
        cmd = (
            "docker cp /tmp/m.sql acc-pg17:/tmp/m.sql && "
            "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F'|' -f /tmp/m.sql"
        )
        out = subprocess.run(["ssh", host, cmd], capture_output=True, text=True)
        if out.returncode != 0:
            sys.stderr.write(out.stderr)
            raise SystemExit("验收脚本执行失败")
        return out.stdout


def parse(output: str) -> dict[str, str]:
    data: dict[str, str] = {}
    for line in output.splitlines():
        if "|" not in line:
            continue
        k, _, v = line.partition("|")
        data.setdefault(k.strip(), v.strip())
    return data


def num(text: str) -> float | None:
    m = re.search(r"-?\d+(?:\.\d+)?", text or "")
    return float(m.group()) if m else None


def last_num(text: str) -> float | None:
    """取文本里**最后一个**数字（用于 '34 rp + 15 canon + 200 raw = 210' 这类复合串）。"""
    nums = re.findall(r"-?\d+(?:\.\d+)?", text or "")
    return float(nums[-1]) if nums else None


def median_retry(text: str) -> float | None:
    """★ M3b 的行是 '2 rows | median_retry_s=300' ⇒ 必须取 median_retry_s，
    ★ **不能**取第一个数字（那是行数）—— ★ 本轮实测踩过这个坑。"""
    m = re.search(r"median_retry_s=(\d+)", text or "")
    return float(m.group(1)) if m else None


def ints(text: str) -> list[int]:
    return [int(x) for x in re.findall(r"\d+", text or "")]


def verdict(ok: bool | None, note: str) -> str:
    if ok is None:
        return f"⚠️ 样本不足/不可判 — {note}"
    return ("✅ 达成 — " if ok else "❌ 未达成 — ") + note


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ssh-host", default="211")
    ap.add_argument("--stage", type=int, default=1, choices=(1, 2))
    ap.add_argument("--inject-guard-fail", action="store_true",
                    help="★ 自测用：把 M16 守卫改成失败 ⇒ 应看到整表裁决作废")
    args = ap.parse_args()

    print(f"== 复采（ssh {args.ssh_host} → acc-pg17）==", flush=True)
    data = parse(run_metrics(args.ssh_host))
    print(f"   指标条数：{len(data)}")

    # ── ★★ 先看量具守卫：守卫不过 ⇒ **整张裁决表作废** ──────────────
    guards = []
    g16 = data.get("M16_join_caliber_check", "")
    g17 = data.get("M17_event_definition_check", "")
    guards.append(("M16 绑定 join 口径", g16))
    guards.append(("M17 事件定义", g17))
    if args.inject_guard_fail:
        # ★ 自测：模拟守卫失败（★ 只改判定，不改采集）
        g16 = "used=13563 ref=99999 | ★★ **量具漂移（注入）**"
        guards[0] = ("M16 绑定 join 口径", g16)
    bad = [g for g in guards if "★★" in g[1]]
    print("\n== 量具守卫（★ 守卫不过 ⇒ 下列所有裁决作废）==")
    for name, val in guards:
        mark = "✔" if "★★" not in val else "★★"
        print(f"   {mark} {name}: {val}")
    if bad:
        print("\n★★ **量具守卫未通过 ⇒ 全部读数不可信 ⇒ 立即修口径，勿采信下面的裁决表**")

    rows: list[tuple[str, str, str, str]] = []   # 判据 | 基线 | 观测 | 裁决

    # ── 资格面 / 成本面（Stage 1 可判）──────────────────────────────
    def scalar(key: str, label: str):
        obs = data.get(key)
        if obs is None:
            rows.append((label, "—", "—", verdict(None, "指标缺失（脚本未产出该键）")))
            return
        v = num(obs)
        base = BASELINES.get(key, (None, "", ""))[0]
        if key == "M2_featured_static":
            # ★ '34 rp + 15 canon + 200 raw_variant = 210' ⇒ 取**最后一个**数字
            v_last = last_num(obs)
            ok = None if v_last is None else int(v_last) == 210
            rows.append((label, "210", obs, verdict(ok, f"static={v_last}")))
        elif key == "M6_free_featured_pairs":
            n = ints(obs)[0] if ints(obs) else None
            ok = None if n is None else n >= 80
            rows.append((label, ">=80", obs, verdict(ok, "★ 资格面，不是行为面")))
        elif key == "M9_missing_raw_variant":
            n = ints(obs)[0] if ints(obs) else None
            ok = None if n is None else n == 0
            rows.append((label, "==0", obs, verdict(ok, "覆盖无缺口" if ok else "★ 有缺口 ⇒ 展开已退化")))
        elif key == "M5_probe_runs_24h":
            n = ints(obs)[0] if ints(obs) else None
            ok = None if n is None else n <= 9334
            rows.append((label, "<=9334", obs, verdict(ok, "★ 爆炸半径未爆")))

    scalar("M2_featured_static", "M2 featured static 规模")
    scalar("M6_free_featured_pairs", "M6 免费面 featured 命中")
    scalar("M9_missing_raw_variant", "M9 展开覆盖缺口")
    scalar("M5_probe_runs_24h", "M5 24h 探测量")

    # ── M3b 机制证据：backoff 档位 2:1 ────────────────────────────────
    # ★ 取 median_retry_s（★ 不是行数）
    f429 = median_retry(data.get("M3b_backoff_ns_featured_err_http_429", ""))
    n429 = median_retry(data.get("M3b_backoff_ns_not_err_http_429", ""))
    f503 = median_retry(data.get("M3b_backoff_ns_featured_err_http_503", ""))
    n503 = median_retry(data.get("M3b_backoff_ns_not_err_http_503", ""))
    if None not in (f429, n429, f503, n503):
        ok = (f429 <= 1800 and n429 >= 3600) or (f503 <= 1800 and n503 >= 3600)
        rows.append(("M3b namespaced backoff 档位", "featured<=1800 / 非featured>=3600",
                     f"429: {f429:.0f} vs {n429:.0f}; 503: {f503:.0f} vs {n503:.0f}",
                     verdict(ok, "★ featured 乘数已作用到 namespaced（2:1）" if ok
                     else "★ 档位未呈 2:1 ⇒ 乘数可能未作用到 namespaced")))
    else:
        rows.append(("M3b namespaced backoff 档位", "—", "—", verdict(None, "档位读数缺失")))

    # ── M12s：复检节奏（群体）────────────────────────────────────────
    for grp, label, bkey in (
        ("ns_feat", "M12s ns featured 复检间隔（med/p90/p99）", "M12s_ns_feat_pre"),
        ("ns_not", "M12s ns 非featured 复检间隔（med/p90/p99）", "M12s_ns_not_pre"),
    ):
        # ★ 基线文字一律从 BASELINES 取（★ 硬编码字面量会和基线脱钩 —— 本轮踩过）
        _b = BASELINES[bkey][0]
        base_txt = f"pre {_b[0]} / {_b[1]} / {_b[2]}"
        key = f"M12s_settled_post_{grp}"
        obs = data.get(key)
        if obs is None:
            rows.append((label, base_txt, "—", verdict(None, "★ post 段无事件（尚未结算）")))
            continue
        vals = ints(obs)
        n = vals[0] if vals else None
        med = vals[1] if len(vals) > 1 else None
        p90 = vals[2] if len(vals) > 2 else None
        p99 = vals[3] if len(vals) > 3 else None
        _pre = BASELINES[bkey][0]
        if grp == "ns_feat":
            # ★ post 的 ns|feat **混入了新覆盖的冷对** ⇒ 整体中位不是正确判据；
            #   ★★ 可判的是「尾部没有变差」（p90 / p99 不劣于 pre）。
            if n is None or n < 20:
                rows.append((label, base_txt, obs,
                             verdict(None, f"★ post n={n} < 20 ⇒ 尾部读数不稳（方向可读）")))
            else:
                ok = (p90 is not None and p90 <= _pre[1]) and (p99 is not None and p99 <= _pre[2])
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ post p90={p90} / p99={p99}（pre {_pre[1]}/{_pre[2]}）"
                                       f"⇒ 尾部{'未变差' if ok else '变差'}"
                                       "（★ 中位不作判据：post 混入了新覆盖的冷对）")))
        else:
            # ★ 对照项：非 featured 的复检节奏**不该变**（变了说明还有别的因素在动）
            if n is None or n < 20:
                rows.append((label, base_txt, obs,
                             verdict(None, f"★ post n={n} < 20 ⇒ 对照读数不稳")))
            else:
                ok = med is not None and abs(med - _pre[0]) <= max(30, int(_pre[0] * 0.2))
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ 对照：post med={med}（pre {_pre[0]}，容差 ±20%）"
                                       f"⇒ {'非featured 节奏未变' if ok else '★ 对照也变了 ⇒ 有其它因素'}")))

    # ── M15：三个冷金丝雀逐对 ────────────────────────────────────────
    canary_lines = []
    for pair in CANARIES:
        pre = data.get(f"M15_pre__{pair}")
        post = data.get(f"M15_post__{pair}")
        pre_med = ints(pre)[1] if pre and len(ints(pre)) > 1 else None
        post_n = ints(post)[0] if post else None
        post_med = ints(post)[1] if post and len(ints(post)) > 1 else None
        canary_lines.append(
            f"{pair}: pre med={pre_med} (n={ints(pre)[0] if pre else '?'})"
            f" → post med={post_med} (n={post_n})")
        if post_med is None:
            verdict_txt = verdict(None, "★ post 尚无已结算事件")
        elif post_n is not None and post_n < 5:
            verdict_txt = verdict(None, f"★ post n={post_n} < 5 ⇒ 幅度不足（方向可读）")
        else:
            ok = post_med <= CANARY_POST_MAX_MED
            verdict_txt = verdict(ok, f"post med={post_med}（期望 ≤{CANARY_POST_MAX_MED}）")
        rows.append((f"M15 金丝雀 {pair}", f"pre med={CANARY_PRE_MED} / 期望 post ≤{CANARY_POST_MAX_MED}",
                     f"pre_med={pre_med} post_med={post_med} post_n={post_n}", verdict_txt))
    if canary_lines:
        print("\n== M15 逐对明细 ==")
        for line in canary_lines:
            print("   " + line)

    # ── M14：目标人群（群体层）—— ★ 参考项：内部两极 ⇒ 逐对看 M15 ──────
    for era in ("pre", "post"):
        obs = data.get(f"M14_{era}_newly_covered")
        if obs:
            vals = ints(obs)
            rows.append((f"M14 目标人群（{era} 段）· newly_covered", "pre 461 events / med 2 / p90 360",
                         obs, verdict(None, "★ 参考项：群体内部两极（热对 vs 冷对）⇒ 幅度看 M15")))

    # ── M11：episode 层症状（★ Stage 1 样本不足 ⇒ 不外推）────────────
    for tag, label, bkey in (("namespaced", "M11 namespaced 恢复感知时延", "M11_ns_pre"),
                             ("bare", "M11 bare 恢复感知时延", "M11_bare_pre")):
        base = BASELINES[bkey][0]
        key = f"M11b_post_fix_{tag}"
        obs = data.get(key)
        if obs is None:
            rows.append((label, f"pre med/p90/max = {base[0]}/{base[1]}/{base[2]}", "—",
                         verdict(None, "★ post 段无 episode")))
            continue
        # ★ M11 的样本率（修正 join 后 ≈7/日）⇒ Stage 1 也能判
        vals = ints(obs)
        n = vals[0] if vals else None
        med = vals[1] if len(vals) > 1 else None
        p90 = vals[2] if len(vals) > 2 else None
        mx = vals[3] if len(vals) > 3 else None
        if n is None or n < 5:
            rows.append((label, f"pre med/p90/max = {base[0]}/{base[1]}/{base[2]}",
                         f"n={n} med={med} p90={p90} max={mx}",
                         verdict(None, f"★ post episodes={n} < 5 ⇒ **样本不足以判定**，★ 不许外推")))
        else:
            ok = (med is not None and p90 is not None and mx is not None
                  and med <= base[0] and p90 <= base[1] and mx <= base[2])
            rows.append((label, f"pre med/p90/max = {base[0]}/{base[1]}/{base[2]}",
                         f"n={n} med={med} p90={p90} max={mx}",
                         verdict(ok, "★ 逐项不劣于 pre 即达成")))

    # ── M12p：待复检护栏 ─────────────────────────────────────────────
    obs = data.get("M12p_pending_post_ns_not")
    if obs:
        rows.append(("M12p ns 非featured 待复检率",
                     f"pre {BASELINES['M12p_pending_ns_not_pre'][0]}%",
                     obs, verdict(None, "★ 护栏项：若 post 显著升高 ⇒ 说明复检反而变慢")))

    if args.stage == 2:
        print("★ Stage 2（7 天窗口）：M11 episode 层与 M15 金丝雀为**主判据**"
              "（★ Stage 1 的「样本不足」项在此应全部可判）。")
    else:
        print("★ Stage 1（24h 窗口）：M11 若 post episode ≥5 即可判"
              "（★ 修正 join 后样本率 ≈7/日）；M15 逐对幅度仍不足（≈2/对/日）⇒ 方向可读。")
    print(f"\n== 裁决表（Stage {args.stage}；era 切点 {EPOCH}）==")
    w = [34, 30, 46, 62]
    print(" | ".join(h.ljust(w[i]) for i, h in enumerate(["判据", "基线", "观测", "裁决"])))
    print("-" * (sum(w) + 12))
    for r in rows:
        cells = [str(c) for c in r]
        if bad:
            cells[3] = "★★ **量具守卫未通过 ⇒ 本行裁决作废**"
        print(" | ".join(cells[i].ljust(w[i]) for i in range(len(cells))))

    print("\n★ 诚实边界（必须一起写进结论）：")
    print("   ① M1 / M1b 可用率**一律不作判据**（归因不可分 + 相位敏感："
          "credential_recovery 扫描 6h 周期 ⇒ 采样须标注「距上次 sweep 多久」）。")
    print("   ② era2 的增量效果是「消除资格障碍」的**潜在收益**；"
          "目标人群（106 个新覆盖变体）修正 join 后 7 天有 **461 事件 / 9 个对"
          "（≈66 事件/日）**，但其中四个「冷金丝雀」各仅 13 次（≈1.9/日）"
          "⇒ ★★ 群体层可判（M14），★ 逐对幅度仍需 Stage 2。")
    print("   ③ M12 post 段存在**右删失**（漏掉复检慢的对 ⇒ 偏差方向对我们有利）"
          "⇒ 已用 M12s（结算版）+ M12p（护栏）对冲，但**必须在报告里披露**。")
    print("   ④ M6 是**资格面**，不是「行为已改善」的证据。")
    print("   ⑤ ★★ 基线口径已在 2026-11-10 14:2x 修正：原先的绑定过滤用 "
          "`provider_model_id = (… LIMIT 1)`，而 raw_model_name 是**一对多**"
          "⇒ 只统计到 30% 事件（4,073 vs 13,555）⇒ ★ 现全部基线均为**修正后**口径。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
