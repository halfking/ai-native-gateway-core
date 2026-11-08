#!/usr/bin/env python3
"""market-price-compare.py —— 市场行情 vs 我方价格对比（运营审计轮项3，2026-11-09）。

数据源（211 真库，只读）：
  · 市场侧：wa-pg17 / salesdecorate.price_observation（25 平台、每日采集、历史不删）
  · 我方侧：acc-pg17 / llm_gateway
      - 零售价：model_credit_rates（手工零售价，积分/1M）
      - 实付价：credential_model_bindings.unit_price_*_per_1m（供应商实付，USD/CNY）

对比口径（格赖斯式：只报能成立的事实）：
  · 归一：小写、去 _-. 空点（salesdecorate 的 raw id 与网关 raw_model_name 的字形差）
  · 市场价 = 该模型全部观测的「当日最低 USD 折算」（input/output 两轴）
  · 我方实付 = 同名绑定的 unit_price（币种如实标注，不做汇率折算——币种未知拒绝不默认）
  · 比值 = 我方实付 / 市场最低（>1 = 买贵了；只对两轴同为 USD 报比值）
用法：
  python scripts/audit/market-price-compare.py [--ssh 211] [--out docs/audit/...]
"""
import argparse
import csv
import io
import json
import re
import subprocess
import sys
from collections import defaultdict


def ssh_query(host: str, cmd: str) -> str:
    out = subprocess.run(
        ["ssh", "-o", "BatchMode=yes", host, cmd],
        capture_output=True, timeout=120, check=True,
    )
    # Windows 宿主默认 GBK —— SSH 回程是 UTF-8（模型名/币种符号），显式解码。
    return out.stdout.decode("utf-8", errors="replace")


def norm(name: str) -> str:
    return re.sub(r"[\s_\-\.]+", "", (name or "").lower())


def fetch_market(host: str) -> dict:
    sql = (
        "docker exec wa-pg17 psql -U salesdecorate -d salesdecorate -tAc \""
        "SELECT o.raw_model_name, min(o.cny_input), min(o.cny_output), max(o.run_date), "
        "min(o.input_price), min(o.output_price), min(o.currency) "
        "FROM price_observation o "
        "WHERE (o.input_price IS NOT NULL OR o.output_price IS NOT NULL) AND o.raw_model_name <> '' "
        "GROUP BY 1\""
    )
    rows = []
    for line in ssh_query(host, sql).splitlines():
        parts = [p.strip() for p in line.split("|")]
        if len(parts) != 7 or not parts[0]:
            continue
        rows.append(parts)
    return {norm(r[0]): r for r in rows}


def fetch_ours(host: str) -> dict:
    sql = (
        "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tAc \""
        "SELECT pm.raw_model_name, b.unit_price_in_per_1m, b.unit_price_out_per_1m, b.currency, b.billing_mode "
        "FROM credential_model_bindings b JOIN provider_models pm ON pm.id = b.provider_model_id "
        "WHERE b.unit_price_in_per_1m IS NOT NULL OR b.unit_price_out_per_1m IS NOT NULL\""
    )
    out = defaultdict(list)
    for line in ssh_query(host, sql).splitlines():
        parts = [p.strip() for p in line.split("|")]
        if len(parts) != 5 or not parts[0]:
            continue
        out[norm(parts[0])].append(parts)
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ssh", default="211")
    ap.add_argument("--out", default="")
    args = ap.parse_args()

    market = fetch_market(args.ssh)
    ours = fetch_ours(args.ssh)
    print(f"market models with price: {len(market)}")
    print(f"our bindings with price : {sum(len(v) for v in ours.values())} ({len(ours)} normalized names)")

    both = sorted(set(market) & set(ours))
    print(f"normalized intersection : {len(both)}")
    lines = []
    for key in both:
        m = market[key]
        for o in ours[key]:
            # 市场最低价取**当日 CNY 折算轴**（salesdecorate 归一口径，跨平台可比）；
            # 原始币种价与币种一并如实附注，不做我方侧的汇率折算。
            min_in, min_out, run_date, src_in, src_out, src_cur = m[1], m[2], m[3], m[4], m[5], m[6]
            our_in, our_out, cur, mode = o[1], o[2], o[3], o[4]
            ratio = ""
            if cur == "USD" and our_in not in ("", "None") and src_in not in ("", "None") and src_cur == "USD" and float(src_in) > 0:
                ratio = f"{float(our_in) / float(src_in):.2f}x"
            lines.append({
                "model": key, "billing_mode": mode, "our_currency": cur,
                "our_in": our_in, "our_out": our_out,
                "market_min_in_cny": min_in, "market_min_out_cny": min_out,
                "market_src_in": src_in, "market_src_out": src_out, "market_src_currency": src_cur,
                "market_asof": run_date, "in_ratio_ours_over_market_src": ratio,
            })
    for l in lines:
        print(" | ".join(f"{k}={v}" for k, v in l.items() if v != ""))
    if args.out:
        with io.open(args.out, "w", encoding="utf-8", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(lines[0].keys()) if lines else ["model"])
            w.writeheader()
            w.writerows(lines)
        print(f"written: {args.out}")
    # 判读（只陈述事实）：比值>1.5 且币种 USD = 超过 2026-11-03 档的 supplier_price_drift 阈值
    flagged = [l for l in lines if l["in_ratio_ours_over_market_src"] and float(l["in_ratio_ours_over_market_src"].rstrip("x")) > 1.5]
    print(f"drift>1.5x (USD in-price): {len(flagged)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
