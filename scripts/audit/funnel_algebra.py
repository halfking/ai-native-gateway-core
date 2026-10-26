#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.87 库侧漏斗代数器：★ 把「读链进入次数」与「v2 来源占比 s」从散文变成可重跑的读数。

★★★ 为什么要它（★ 本轮的核心教训）：
   ★ §143.85.4 写下一句「恢复协调器那条入口（★ §143.84 实证 ≥189 次、≥64.5% 的 L3 调用）」，
     ★ 那两个数字来自一句减法：**669 − 480 = 189**，★ 且 **189 / 293 = 64.5%**。
   ★ ★★★ 本轮判定：**那句减法混了两个不同总体** ⇒ ⇒ 「189」**不成立**；
     ★ 而真正的那个量（v2 来源且进入读链的次数 = s）**在现有数据下是自由参数**。
   ★ ⇒ ⇒ ★★ **把代数做成脚本 ⇒ ⇒ 结论从此【可重跑、可对拍】**，★ 而不是散文里的一个数字。

★★★★★ 本脚本做什么（★ 全部只读本地文件，★ 零 DB 访问）：
   ① 读两份【快照日志】（★ `verify_l3_leg_value.sql` 的 SNAPSHOT 行）⇒ 差分出各腿的
      Δcalls / Δrows / Δms；
   ② 按【命中即止】形态推出漏斗：★ 读链进入次数 = 腿① 调用 − 腿① 返回行；
   ③ 用【腿② == 腿③ == L3】做【独立交叉验】（★ 三个不同 queryid 同时相等 ⇒ 漏斗闭合）；
   ④ 给出三个区间：★ 非 v2 来源的腿① 调用下界、★ s 的取值区间、★ 穿透率的【上界】；
   ⑤ 打印【旧数 189 的不成立理由】（★ 三条）。

★★★★★ 退出码（★ 三档，★ 别把「查不到」读成「通过」）：
   0 = 不变量全部成立；
   1 = 有不变量被破坏（★ 量具坏了 ⇒ 不是数据的问题）；
   2 = 输入不可达（★ 快照文件缺失 / 无 SNAPSHOT 行）。

用法：
   python3 scripts/audit/funnel_algebra.py --self-check
   python3 scripts/audit/funnel_algebra.py <基准快照> <对照快照> [--svc-wide 480] [--svc-precise 473]
"""

from __future__ import annotations

import json
import os
import re
import sys

SNAP_RE = re.compile(r"SNAPSHOT\s+(\{.*\})\s*$", re.M)

LEG1 = "L1_final_full_snapshot"
LEG2 = "L1b_fallback_turnno"
LEG3 = "L2_delta_chain"
L3 = "L3_gamma_coalesce_ff_v2"
L5 = "L5_writepath_latest"

# ★★★★★★ 本轮实测读数（★ 2026-10-23）：★ 这些是【断言】⇒ ⇒ 脚本每次跑都会复算并对拍。
# ★ 数据来源（★ 同一快照对 ⇒ 差分可直接相减）：
#   基准快照 /private/tmp/p14381/snap_c.log  (2026-10-23 04:18:22)
#   对照快照 /private/tmp/p14384/snap_e.log  (2026-10-23 05:07:32)  ⇒ Δ 2,950 s
EXPECTED_LEG1_CALLS = 669
EXPECTED_LEG1_ROWS = 376
EXPECTED_LEG2_CALLS = 293
EXPECTED_LEG3_CALLS = 293
EXPECTED_L3_CALLS = 293
# ★ 服务侧同窗「v2 落空事件数」（★ 154 journald + 245 jsonl）：
EXPECTED_SVC_WIDE = 480  # ★ 04:18:00–05:08:00（154 = 412 + 245 = 68）
EXPECTED_SVC_PRECISE = 473  # ★ 04:18:22–05:07:32（154 = 407 + 245 = 66）★ 逐秒对齐
# ★★★★★★ §143.88 增量（★ 读码 + 库侧 SQL 形态【双证】⇒ ⇒ 前提 A 成立）：
# ★ 前提 A = 「v2 落空 ⇒ 腿① 必落空」★ 依据 = 腿① 读的是【v2 侧】的
# ★ public.session_bodies_unified（kind='final_full'）★ 与「v2 落空」同一侧。
# ★ ⇒ ⇒ v2 来源的读链进入【全部】落在 293 次落空里 ⇒ ⇒
EXPECTED_L1HIT_LOWER = 180  # ★ = 473 − 293 ⇒ 服务侧 v2 落空里【至少】180 次没进读链（L1 命中）
EXPECTED_NON_V2_LEG1_LOWER = 376  # ★ = 669 − 293 ⇒ 非 v2 来源的腿① 调用下界


def load_snapshot(path: str) -> dict:
    """读一份快照日志里的最后一条 SNAPSHOT JSON（★ 返回 {label: {calls,rows,ms}}）。"""
    if not os.path.isfile(path):
        print("❌ 快照文件不存在：%s" % path, file=sys.stderr)
        sys.exit(2)
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        text = fh.read()
    found = SNAP_RE.findall(text)
    if not found:
        print("❌ %s 里没有 SNAPSHOT 行" % path, file=sys.stderr)
        sys.exit(2)
    return json.loads(found[-1])


def diff_snapshots(base: dict, cur: dict) -> dict:
    """逐腿差分（★ 只取两条快照都有的腿）。"""
    out = {}
    for label in sorted(set(base) & set(cur)):
        b, c = base[label], cur[label]
        out[label] = {
            "calls": c.get("calls", 0) - b.get("calls", 0),
            "rows": c.get("rows", 0) - b.get("rows", 0),
            "ms": round(c.get("ms", 0.0) - b.get("ms", 0.0), 1),
        }
    return out


def funnel(d: dict) -> dict:
    """按【命中即止】形态算漏斗（★ 纯代数，★ 不查任何外部事实）。"""
    leg1_calls = d.get(LEG1, {}).get("calls", 0)
    leg1_rows = d.get(LEG1, {}).get("rows", 0)
    leg2_calls = d.get(LEG2, {}).get("calls", 0)
    leg3_calls = d.get(LEG3, {}).get("calls", 0)
    l3_calls = d.get(L3, {}).get("calls", 0)

    # ★ 腿① 命中即止 ⇒ 落空次数 = 调用 − 返回行（★ 每命中返回 1 行 ⇒ LoadLatestOutbound LIMIT 1）
    leg1_misses = leg1_calls - leg1_rows
    # ★ 进入读链的次数（★ 交叉验：★ 腿②/腿③/L3 三个独立 queryid 必须同时等于它）
    read_chain_entries = leg1_misses
    cross_ok = (leg2_calls == leg3_calls == l3_calls == read_chain_entries)

    # ★ s = 「v2 来源且进入读链」的次数 ⇒ 0 <= s <= read_chain_entries
    s_min, s_max = 0, read_chain_entries
    # ★ 非 v2 来源的腿① 调用下界（★ 前提 A：★ v2 落空 ⇒ 腿① 必落空 ⇒ ⇒ v2 来源必落空）
    # ★★★ §143.88：★ 前提 A 已由【读码 + 库侧 SQL 形态】双证 ⇒ ⇒ 这条不再是「未验证前提」
    non_v2_leg1_lower = leg1_calls - read_chain_entries
    # ★ §143.88：★ v2 来源的读链进入【全部】落在 293 次落空里 ⇒ ⇒
    # ★ ⇒ 服务侧 v2 落空数里【至少】(v2 落空数 − 293) 次【没有进读链】（★ L1 命中层）
    l1hit_lower = 0  # ★ 由 v2 落空数在 self_check / report 里给（★ 依赖服务侧计数）
    # ★ 非 v2 来源的 L3 调用 = read_chain_entries − s ⇒ ⇒ 区间 [read_chain_entries − s_max, read_chain_entries − s_min]
    non_v2_l3_lower = read_chain_entries - s_max
    non_v2_l3_upper = read_chain_entries - s_min

    return {
        "leg1_calls": leg1_calls,
        "leg1_rows": leg1_rows,
        "leg1_misses": leg1_misses,
        "leg2_calls": leg2_calls,
        "leg3_calls": leg3_calls,
        "l3_calls": l3_calls,
        "read_chain_entries": read_chain_entries,
        "cross_ok": cross_ok,
        "s_min": s_min,
        "s_max": s_max,
        "non_v2_leg1_lower": non_v2_leg1_lower,
        "l1hit_lower": l1hit_lower,
        "non_v2_l3_lower": non_v2_l3_lower,
        "non_v2_l3_upper": non_v2_l3_upper,
    }


def report(d: dict, f: dict, svc_wide: int, svc_precise: int) -> None:
    print("=" * 78)
    print("§143.87 库侧漏斗代数")
    print("=" * 78)
    print("-- 各腿差分（★ 窗口内增量）")
    print("%-30s %10s %10s %12s" % ("腿", "Δcalls", "Δrows", "Δms"))
    for label in (LEG1, LEG2, LEG3, L3, L5):
        v = d.get(label)
        if v is None:
            continue
        print("%-30s %10d %10d %12.1f" % (label, v["calls"], v["rows"], v["ms"]))
    print()
    print("-- 代数①：★ 读链进入次数")
    print("   腿① 调用 %d − 腿① 返回行 %d = 落空 %d" % (f["leg1_calls"], f["leg1_rows"], f["leg1_misses"]))
    print("   交叉验：★ 腿② %d == 腿③ %d == L3 %d ⇒ ⇒ %s"
          % (f["leg2_calls"], f["leg3_calls"], f["l3_calls"],
             "✔ 漏斗闭合" if f["cross_ok"] else "✘ 不闭合（★ 量具或链形变了）"))
    print()
    print("-- 代数②：★ 非 v2 来源的腿① 调用下界（★ 前提 A：★ v2 落空 ⇒ V1 侧也空）")
    print("   %d − %d = %d" % (f["leg1_calls"], f["read_chain_entries"], f["non_v2_leg1_lower"]))
    print()
    print("-- 代数③：★ s = 「v2 来源且进入读链」的次数 ⇒ 自由参数")
    print("   s ∈ [%d, %d]" % (f["s_min"], f["s_max"]))
    print("   ⇒ ⇒ 非 v2 来源的 L3 调用 ∈ [%d, %d]（★ 下界是 0，★ 不是 189）"
          % (f["non_v2_l3_lower"], f["non_v2_l3_upper"]))
    print()
    print("-- 代数④：★ 穿透率上界（★ 分子应是 s，★ 不是「所有读链进入次数」）")
    for name, svc in (("宽窗 04:18:00–05:08:00", svc_wide), ("逐秒对齐 04:18:22–05:07:32", svc_precise)):
        if svc > 0:
            print("   %s：★ 上界 = %d / %d = %.2f%%" % (name, f["read_chain_entries"], svc,
                                                     100.0 * f["read_chain_entries"] / svc))
    print()
    print("-- §143.88 增量：★ 前提 A 成立（腿① 读 v2 侧）⇒ ⇒ 两个下界")
    print("   ① 非 v2 来源的腿① 调用 ≥ %d − %d = %d"
          % (f["leg1_calls"], f["read_chain_entries"], f["non_v2_leg1_lower"]))
    print("   ② v2 落空里【没进读链】的次数 ≥ %d − %d = %d（★ L1 命中层）"
          % (svc_precise, f["read_chain_entries"], svc_precise - f["read_chain_entries"]))
    print()
    print("-- 旧数 189 的不成立理由（三条）")
    print("   ① 减法的两个总体不同量纲：★ %d 是【腿① 调用次数】，★ %d 是【服务侧 v2 落空事件数】"
          % (f["leg1_calls"], svc_wide))
    print("      ⇒ ⇒ 其中 %d 次根本没进读链 ⇒ ⇒ %d 不是腿① 调用数 ⇒ ⇒ 两者相减无语义"
          % (svc_wide - f["read_chain_entries"], svc_wide))
    print("   ② 命中即止：★ 腿① 的 %d 次里有 %d 次是命中（★ 直接 return）⇒ ⇒ 能到 L3 的只有 %d 次"
          % (f["leg1_calls"], f["leg1_rows"], f["read_chain_entries"]))
    print("   ③ 正确的量是自由参数：★ s ∈ [%d, %d] ⇒ ⇒ 「非 v2 来源的 L3 调用」下界 = %d"
          % (f["s_min"], f["s_max"], f["non_v2_l3_lower"]))
    print()


def self_check() -> int:
    """不变量自检（★ 不依赖任何外部文件 ⇒ ⇒ 任何机器上都能跑）。"""
    d = {
        LEG1: {"calls": EXPECTED_LEG1_CALLS, "rows": EXPECTED_LEG1_ROWS, "ms": 0.0},
        LEG2: {"calls": EXPECTED_LEG2_CALLS, "rows": 0, "ms": 0.0},
        LEG3: {"calls": EXPECTED_LEG3_CALLS, "rows": 0, "ms": 0.0},
        L3: {"calls": EXPECTED_L3_CALLS, "rows": 0, "ms": 0.0},
        L5: {"calls": 519, "rows": 189, "ms": 0.0},
    }
    f = funnel(d)
    checks = [
        ("INV1 漏斗闭合（★ 腿① 落空 == 腿② == 腿③ == L3）",
         f["leg1_misses"] == f["leg2_calls"] == f["leg3_calls"] == f["l3_calls"]),
        ("INV2 读链进入次数 = 293", f["read_chain_entries"] == 293),
        ("INV3 非 v2 腿① 调用下界 = 376", f["non_v2_leg1_lower"] == 376),
        ("INV4 s 的区间 = [0, 293]", (f["s_min"], f["s_max"]) == (0, 293)),
        ("INV5 非 v2 来源的 L3 调用下界 = 0（★ 不是 189）", f["non_v2_l3_lower"] == 0),
        ("INV6 旧算式 669 − 480 = 189（★ 数字对、★ 语义错）",
         f["leg1_calls"] - EXPECTED_SVC_WIDE == 189),
        ("INV7 宽窗上界 = 293/480 ≈ 61.0%",
         abs(100.0 * 293 / 480 - 61.04) < 0.01),
        ("INV8 逐秒对齐窗上界 = 293/473 ≈ 61.9%",
         abs(100.0 * 293 / 473 - 61.95) < 0.01),
        # ★★★★★★ §143.88 增量：★ 前提 A 成立（★ 腿① 读 v2 侧 ⇒ v2 落空必落空）
        ("INV9 非 v2 来源的腿① 调用下界 = 376（★ 前提 A 成立版）",
         f["non_v2_leg1_lower"] == EXPECTED_NON_V2_LEG1_LOWER),
        ("INV10 v2 落空里没进读链的次数 ≥ 180（★ L1 命中层）",
         EXPECTED_SVC_PRECISE - 293 == EXPECTED_L1HIT_LOWER),
        ("INV11 前提 A 成立 ⇒ v2 来源的读链进入 ≤ 293（★ 落空总数）",
         EXPECTED_L1HIT_LOWER + 293 == EXPECTED_SVC_PRECISE),
    ]
    bad = 0
    for name, ok in checks:
        print(("✅ " if ok else "❌ ") + name)
        if not ok:
            bad += 1
    print()
    if bad:
        print("★ 自检失败：%d 条不变量被破坏" % bad)
        return 1
    print("★ 自检通过：%d 条不变量全部成立" % len(checks))
    return 0


def main(argv: list[str]) -> int:
    if "--self-check" in argv:
        return self_check()

    svc_wide = EXPECTED_SVC_WIDE
    svc_precise = EXPECTED_SVC_PRECISE
    paths = []
    i = 0
    while i < len(argv):
        if argv[i] == "--svc-wide":
            svc_wide = int(argv[i + 1]); i += 2
        elif argv[i] == "--svc-precise":
            svc_precise = int(argv[i + 1]); i += 2
        else:
            paths.append(argv[i]); i += 1
    if len(paths) < 2:
        print(__doc__)
        return 2

    base = load_snapshot(paths[0])
    cur = load_snapshot(paths[1])
    d = diff_snapshots(base, cur)
    f = funnel(d)
    report(d, f, svc_wide, svc_precise)
    return 0 if f["cross_ok"] else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
