#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.90 跨源求交器：★ 从【服务侧三类日志】直接算出 ⓪ 的适用面 A 与 s（L3 里 v2 来源的次数）。

★★★ 为什么需要它（★ runbook §143.88.5 / §143.89.3）：★★
   ★ s ＝ 「库端 L3 调用里，来自 v2 来源的那部分」。
   ★ ★★ 库端【没有】session 级痕迹 ⇒ ⇒ 只能靠【服务侧】按 session_id 求交。
   ★ ★★ 而 ⓪ 的适用面 A ＝ 「被 §143.86 短掉的读链进入次数」＝ 服务侧跳过日志数。
   ★ ⇒ ⇒ 本脚本把这两件事做成【可重跑、可验证】的一次计算。

★★★★★ 输入（★ 全部是服务侧日志；★ 库侧读数可选，用于对拍）：
   --v2-miss FILE   「v2 cache loaded successfully」且 body_size == 0 的日志
                    （★ 这是「v2 落空」事件；★ 154 用 journalctl -o short-iso，★ 245 用 jsonl）
   --l3 FILE        「read chain reached L3」的日志（★ §143.89 的埋点）
   --skip FILE      「skip db load, auto-created session」的日志（★ §143.86 的跳过埋点）
   --format         auto | journald | jsonl（★ auto ＝ 按内容嗅探）
   --since / --until   同窗过滤（★ ISO8601；★ ★★ 两侧必须同窗，否则比值失去意义）
   --db-l3-calls N  库端同一窗口的 L3 Δcalls（★ 可选；★ 给了就做对拍）

★★★★★ 输出：
   · N_v2（v2 落空事件数）、N_l3（走到 L3 的事件数）、A（跳过数）
   · s ＝ |{v2 落空} ∩ {走到 L3}|（★ ★★ 多重集交，★ 不是集合交）
   · A 的两个占比：A / N_l3（★ L3 口径）、A / N_v2（★ v2 落空口径）
   · 若给了 --db-l3-calls ⇒ ⇒ 对拍：服务侧 N_l3 vs 库端 Δcalls 的差值

★★★★★ ★★ 为什么必须用【多重集】而不是集合（★ 本轮的核心正确性点）：
   ★ 同一会话可能【多次】进入读链（★ 重试 / 多轮请求）⇒ ⇒ 库端的 calls 也是【次数】。
   ★ ⇒ ⇒ 若用集合交，★ 重试多的会话会被【低估】⇒ ⇒ s 偏小 ⇒ ⇒ 收益被低估。
   ★ ⇒ ⇒ 本脚本一律用 Counter（★ 其 `&` 运算就是 min-count 的多重集交）。

★★★ 用法：
   python3 scripts/audit/cross_source_intersect.py --self-check
   python3 scripts/audit/cross_source_intersect.py \
       --v2-miss v2.log --l3 l3.log --skip skip.log \
       --since 2026-10-23T08:00:00+08:00 --until 2026-10-23T09:00:00+08:00 \
       --db-l3-calls 293
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from datetime import datetime

V2_MISS_MARK = "v2 cache loaded successfully"
L3_MARK = "read chain reached L3"
SKIP_MARK = "skip db load, auto-created session"

# journalctl -o short-iso 的行首时间戳：2026-10-23T08:00:00+0800
_JOURNALD_TS_RE = re.compile(r"^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[+-]\d{4}|Z)?)\s")
_SESSION_RE = re.compile(r"\bsession=([^\s\"]+)")
_BODY_SIZE_RE = re.compile(r"\bbody_size=(\d+)")


def _parse_ts(raw: str):
    if not raw:
        return None
    raw = raw.strip().replace("Z", "+00:00")
    # ★ +0800 ⇒ +08:00（★ Python 的 fromisoformat 在 3.7~3.10 不认 +0800）
    if re.search(r"[+-]\d{4}$", raw):
        raw = raw[:-5] + raw[-5:-2] + ":" + raw[-2:]
    try:
        return datetime.fromisoformat(raw)
    except ValueError:
        return None


def _iter_events(path: str, fmt: str):
    """逐行产出 (ts, session, body_size, line)。"""
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line.strip():
                continue
            ts = None
            session = None
            body_size = None

            # ★★★ 【journald 的真实形态是「journald 头 + JSON payload」】—— ★ 例：
            #   2026-10-23T04:18:22+0800 host env[5841]: {"time":"...","msg":"...","session":"...","body_size":0}
            # ★ ⇒ ⇒ 【只认「行首是 {」】的嗅探会把整份日志解析成 0 条 ⇒ ⇒ ★★ 必须行内取 JSON。
            d = None
            if fmt == "jsonl" or fmt == "auto":
                i = line.find("{")
                j = line.rfind("}")
                if i >= 0 and j > i:
                    try:
                        d = json.loads(line[i:j + 1])
                    except Exception:
                        d = None
            if d is not None:
                ts = _parse_ts(str(d.get("time") or d.get("timestamp")
                                  or d.get("@timestamp") or d.get("ts") or ""))
                session = d.get("session")
                bs = d.get("body_size")
                body_size = int(bs) if isinstance(bs, (int, float)) else None
                mark = str(d.get("msg") or d.get("message") or line)
            else:
                m = _JOURNALD_TS_RE.match(line)
                if m:
                    ts = _parse_ts(m.group(1))
                ms = _SESSION_RE.search(line)
                if ms:
                    session = ms.group(1)
                mb = _BODY_SIZE_RE.search(line)
                if mb:
                    body_size = int(mb.group(1))
                mark = line

            if session is None:
                continue
            yield ts, str(session), body_size, mark


def collect(path: str, mark: str, fmt: str, since, until, require_zero_body: bool = False) -> Counter:
    """收集某一类事件 ⇒ 返回 session ⇒ 次数 的【多重集】。"""
    c: Counter = Counter()
    if not path:
        return c
    for ts, session, body_size, mark_text in _iter_events(path, fmt):
        if mark not in mark_text:
            continue
        if require_zero_body and body_size not in (0, None):
            continue
        if since is not None and (ts is None or ts < since):
            continue
        if until is not None and (ts is None or ts > until):
            continue
        c[session] += 1
    return c


def multisect(a: Counter, b: Counter) -> Counter:
    """多重集交（★ Counter 的 & ＝ min-count 交）。"""
    return a & b


def self_check() -> int:
    """合成夹具自检（★ 不依赖任何生产数据 ⇒ 任何机器上都能跑）。"""
    v2 = Counter({"a": 2, "b": 1})
    l3 = Counter({"a": 2, "c": 1})
    skip = Counter({"a": 1})

    s = multisect(v2, l3)
    checks = [
        ("s ＝ 2（★ a 出现 2 次 ⇒ 多重集交保留 min(2,2)）", s["a"] == 2),
        ("s 里没有 b（★ b 不在 l3）", "b" not in s),
        ("s 里没有 c（★ c 不在 v2）", "c" not in s),
        ("★ 集合交会得到 1 ⇒ 与多重集结果不同（★ 这就是本工具必须用多重集的理由）",
         len(set(v2) & set(l3)) == 1 and s["a"] == 2),
        ("A ＝ 1", sum(skip.values()) == 1),
        ("A / N_l3 ＝ 1/3", abs(sum(skip.values()) / sum(l3.values()) - 1.0 / 3.0) < 1e-9),
        ("A / N_v2 ＝ 1/3", abs(sum(skip.values()) / sum(v2.values()) - 1.0 / 3.0) < 1e-9),
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


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="★ §143.90 跨源求交器")
    ap.add_argument("--v2-miss", default="")
    ap.add_argument("--l3", default="")
    ap.add_argument("--skip", default="")
    ap.add_argument("--format", default="auto", choices=["auto", "journald", "jsonl"])
    ap.add_argument("--since", default="")
    ap.add_argument("--until", default="")
    ap.add_argument("--db-l3-calls", type=int, default=-1)
    ap.add_argument("--self-check", action="store_true")
    args = ap.parse_args(argv)

    if args.self_check:
        return self_check()

    since = _parse_ts(args.since) if args.since else None
    until = _parse_ts(args.until) if args.until else None
    if args.since and since is None:
        print("❌ --since 解析失败：%s" % args.since, file=sys.stderr)
        return 2
    if args.until and until is None:
        print("❌ --until 解析失败：%s" % args.until, file=sys.stderr)
        return 2

    v2 = collect(args.v2_miss, V2_MISS_MARK, args.format, since, until, require_zero_body=True)
    l3 = collect(args.l3, L3_MARK, args.format, since, until)
    skip = collect(args.skip, SKIP_MARK, args.format, since, until)

    s = multisect(v2, l3)
    n_v2, n_l3, n_a = sum(v2.values()), sum(l3.values()), sum(skip.values())

    print("=" * 78)
    print("§143.90 跨源求交（★ 同窗：%s .. %s）" % (args.since or "(不限)", args.until or "(不限)"))
    print("=" * 78)
    print("N_v2（v2 落空事件）      = %d" % n_v2)
    print("N_l3（走到 L3 的事件）    = %d" % n_l3)
    print("A（⓪ 跳过的读链进入）      = %d" % n_a)
    print("s（v2 ∩ 走到 L3，多重集）  = %d" % sum(s.values()))
    if n_l3:
        print("A / N_l3（L3 口径）       = %.2f%%" % (100.0 * n_a / n_l3))
    if n_v2:
        print("A / N_v2（v2 落空口径）  = %.2f%%" % (100.0 * n_a / n_v2))
    if n_l3 and n_v2:
        print("s / N_l3（★ s 的上界）   = %.2f%%" % (100.0 * sum(s.values()) / n_l3))
    if args.db_l3_calls >= 0:
        diff = n_l3 - args.db_l3_calls
        print()
        print("★ 对拍：服务侧 N_l3 = %d vs 库端 L3 Δcalls = %d ⇒ ⇒ 差值 = %+d"
              % (n_l3, args.db_l3_calls, diff))
        print("  ★ 差值应当 ≈ 0（★ 差得多 ⇒ 说明两侧【不同窗】或【一侧有漏】⇒ ⇒ 先查窗，再查漏）")
    if not v2 and not l3:
        print()
        print("⚠ 两个集合都是空的 —— ★ 「0 命中的第一嫌疑是量具坏了」⇒ ⇒ 先确认日志来源与标记串")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
