#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
=====================================================================
§143.81 跨运行快照差分器：★ 把【累计量】换成【增量量】

★★★★ 为什么要它（★ 本轮的核心教训）：
   ★ pg_stat_statements 的 calls / rows / total_exec_time 只增不减
   ★ ⇒ 「累计值大」≠「现在还在变大」
   ★ ⇒ §143.79 / §143.80 曾把【4.1 天的历史累计】当成【现时成本】
   ★ ⇒ ⇒ 「读链 74.4 分钟、L3 占 88.1%」的前提被推翻
   ★ ⇒ ⇒ 【判定「此刻还在不在跑」必须看增量】

★★★ 本工具做什么：
   输入 1..N 份快照日志（★ 由 scripts/audit/run_prod_sql.sh 落盘）
   ⇒ 提取每份里的 `SNAPSHOT {json}` 行（★ verify_l3_leg_value.sql 的 Q1 末尾输出）
   ⇒ 对【最早】与【最新】两份做差 ⇒ 输出每条腿的
        Δcalls / Δrows / Δms / Δms-per-call / ALIVE(此刻还在不在跑)
   ⇒ 并给出【两个口径并列】（★ 这是本轮的关键修法）：
        ① 【累计口径】= 累计 ms（★ 含历史 ⇒ 不可直接当现时成本）
        ② 【现时口径】= 窗口内 ms（★ 两次快照之差 ⇒ 才是现时）

★★★ 通用式（★ 与 §143.80 的「跨面交叉验证」同族）：
   ★ 「现时成本」这类结论，【必须】写成【窗口 × 速率】的形式，
     并把【窗口长度】与【采样是否同源】一起发布。
   ★ 若窗口太短 ⇒ 低频腿可能被采成 delta=0 ⇒ ⇒ 必须用【长窗口】复核
     （★ 本工具读多份快照 ⇒ 窗口可任意拉长 ⇒ ⇒ 不受单次会话 60s 限制）。

用法：
   python3 scripts/audit/diff_l3_snapshots.py snap_a.log snap_b.log [snap_c.log ...]
   python3 scripts/audit/diff_l3_snapshots.py --json snap_a.log snap_b.log
"""

from __future__ import annotations

import json
import os
import re
import sys
from typing import Any

SNAP_RE = re.compile(r"SNAPSHOT\s+(\{.*\})\s*$", re.M)


def load_snapshots(paths: list[str]) -> list[tuple[str, dict[str, Any]]]:
    snaps: list[tuple[str, dict[str, Any]]] = []
    for path in paths:
        if not os.path.isfile(path):
            print(f"⚠ 跳过（文件不存在）: {path}", file=sys.stderr)
            continue
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        found = SNAP_RE.findall(text)
        if not found:
            print(f"⚠ 跳过（无 SNAPSHOT 行）: {path}", file=sys.stderr)
            continue
        for raw in found:
            try:
                snaps.append((path, json.loads(raw)))
            except json.JSONDecodeError as exc:
                print(f"⚠ 跳过（SNAPSHOT JSON 解析失败 {exc}）: {path}", file=sys.stderr)
    return snaps


def _num(snap: dict[str, Any], label: str, key: str) -> float:
    return float(snap.get(label, {}).get(key, 0) or 0)


def diff(first: dict[str, Any], last: dict[str, Any]) -> list[dict[str, Any]]:
    labels = sorted(set(first) | set(last))
    rows: list[dict[str, Any]] = []
    for label in labels:
        c0, c1 = _num(first, label, "calls"), _num(last, label, "calls")
        r0, r1 = _num(first, label, "rows"), _num(last, label, "rows")
        m0, m1 = _num(first, label, "ms"), _num(last, label, "ms")
        dcalls = c1 - c0
        dms = m1 - m0
        rows.append(
            {
                "label": label,
                "cumul_calls": c1,
                "cumul_ms": m1,
                "delta_calls": dcalls,
                "delta_rows": r1 - r0,
                "delta_ms": dms,
                "delta_ms_per_call": (dms / dcalls) if dcalls else 0.0,
                "alive": dcalls > 0,
            }
        )
    rows.sort(key=lambda r: r["delta_ms"], reverse=True)
    return rows


def render(rows: list[dict[str, Any]], first_path: str, last_path: str) -> None:
    print("=" * 100)
    print("§143.81 增量差分：★ 累计量 ≠ 现时成本")
    print(f"  基准快照: {first_path}")
    print(f"  对照快照: {last_path}")
    print("=" * 100)
    hdr = (
        f"{'label':<28} {'cumul_calls':>12} {'cumul_ms':>14} "
        f"{'Δcalls':>8} {'Δrows':>8} {'Δms':>12} {'Δms/call':>10} {'ALIVE':>6}"
    )
    print(hdr)
    print("-" * len(hdr))
    for r in rows:
        print(
            f"{r['label']:<28} {r['cumul_calls']:>12.0f} {r['cumul_ms']:>14.1f} "
            f"{r['delta_calls']:>8.0f} {r['delta_rows']:>8.0f} {r['delta_ms']:>12.1f} "
            f"{r['delta_ms_per_call']:>10.2f} {'是' if r['alive'] else '否(死)':>6}"
        )

    alive = [r for r in rows if r["alive"]]
    dead = [r for r in rows if not r["alive"]]
    cumul_total = sum(r["cumul_ms"] for r in rows)
    win_total = sum(r["delta_ms"] for r in rows)
    dead_cumul = sum(r["cumul_ms"] for r in dead)
    alive_cumul = sum(r["cumul_ms"] for r in alive)
    alive_win = sum(r["delta_ms"] for r in alive)

    def pct(part: float, whole: float) -> float:
        """★ 安全占比（★ 分母为 0 时返回 0.0 ⇒ 不抛 ZeroDivisionError）。"""
        return (part / whole * 100.0) if whole else 0.0

    print("-" * 100)
    print("★ 两个口径（★ 必须并列发布 ⇒ 缺一个就会被读成另一个）:")
    print(f"  ① 累计口径（★ 含历史，不可直接当现时成本）: {cumul_total:,.1f} ms")
    print(f"  ② 现时口径（★ 窗口内增量）              : {win_total:,.1f} ms")
    if cumul_total > 0:
        print(
            f"  ⇒ 现时占累计的比例: {pct(win_total, cumul_total):.2f}%"
            f"  ★⇒ 这就是「把累计当现时」会把成本高估的倍数来源"
        )
    print("-" * 100)
    print("★ 已死腿（★ delta_calls=0 ⇒ 累计值是历史成本）:")
    for r in dead:
        print(
            f"  - {r['label']:<28} 累计 {r['cumul_calls']:>8.0f} 次 / {r['cumul_ms']:>12.1f} ms"
            f"  ⇒ 占累计口径 {pct(r['cumul_ms'], cumul_total):.2f}%"
        )
    if dead_cumul > 0:
        print(
            f"  ⇒ ★ 已死腿合计占累计口径 {pct(dead_cumul, cumul_total):.2f}%"
            f"（{dead_cumul:,.1f} ms）★ ⇒ 这部分【不在现时成本里】"
        )
    print("★ 活腿（★ 此刻还在跑）:")
    for r in alive:
        print(
            f"  - {r['label']:<28} 窗口内 {r['delta_calls']:>8.0f} 次 / {r['delta_ms']:>12.1f} ms"
            f"  ⇒ 占现时口径 {pct(r['delta_ms'], win_total):.2f}%"
        )
    if alive_cumul > 0:
        print(
            f"  ⇒ ★ 活腿占累计口径 {pct(alive_cumul, cumul_total):.2f}%"
            f"（{alive_cumul:,.1f} ms）★ ⇒ ★【收益重估只能在这部分上做】"
        )
    print("=" * 100)


def main(argv: list[str]) -> int:
    args = argv[1:]
    as_json = False
    if args and args[0] == "--json":
        as_json = True
        args = args[1:]
    if len(args) < 2:
        print(__doc__)
        return 2
    snaps = load_snapshots(args)
    if len(snaps) < 2:
        print("✗ 至少需要两份含 SNAPSHOT 行的快照才能做差", file=sys.stderr)
        return 1
    first_path, first = snaps[0]
    last_path, last = snaps[-1]
    rows = diff(first, last)
    if as_json:
        print(
            json.dumps(
                {
                    "first": first_path,
                    "last": last_path,
                    "rows": rows,
                    "cumul_total_ms": sum(r["cumul_ms"] for r in rows),
                    "window_total_ms": sum(r["delta_ms"] for r in rows),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        render(rows, first_path, last_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
