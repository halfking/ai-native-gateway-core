#!/usr/bin/env python3
"""market-price-compare.py —— 市场行情 vs 我方价格对比（运营审计轮项3，2026-11-09；
A-17 双币制升级 2026-11-12 收口轮 = 定稿 M2-T5）。

数据源（211 真库，只读）：
  · 市场侧：acc-pg17 / salesdecorate.price_observation（25 平台、每日采集、历史
    不删）——★ 2026-11-12 修：wa-pg17 容器已退役、salesdecorate 库并入 acc-pg17
    实例（collector SD_DB_HOST=192.168.31.211:15432 即 acc-pg17 宿主端口）。
  · 我方侧：acc-pg17 / llm_gateway
      - 零售价：model_credit_rates（手工零售价，积分/1M）
      - 实付价：credential_model_bindings.unit_price_*_per_1m（供应商实付，USD/CNY）
      - ★ A-17（958/959）：market_platform_registry（区域/币种裁定权威）——
        双地板按 registry 裁定分组，未登记平台不计入任何地板（与同步器同规）。

对比口径（格赖斯式：只报能成立的事实；A-17 双币制）：
  · 归一：小写、去 _-. 空点（salesdecorate 的 raw id 与网关 raw_model_name 的字形差）
  · 双地板（区域原生币，registry 裁定）：overseas = USD 原生最低、
    domestic = CNY 原生最低；综合（CNY 折算轴）= 全部观测的 cny_* 最低
    （salesdecorate 采集时点折算，跨平台可比，旧口径不变）
  · 我方实付 = 同名绑定的 unit_price（币种如实标注，不做汇率折算——币种未知拒绝不默认）
  · 比值 = 我方实付 / 同币种地板（USD 实付 vs 海外地板、CNY 实付 vs 综合地板；
    >1 = 买贵了；币种不成立就不报比值——不做隐式折算）
    ★ 旧版比值腿的缺陷：min(input_price) 跨 USD/CNY 混合取最小 ⇒ 币种错配的
    「比值」是纯噪声（941 视图同款纪律）；A-17 双地板列落地后按同币种成立。
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


def fetch_registry(host: str) -> dict:
    """A-17 区域裁定权威（llm_gateway.market_platform_registry，958/959）。

    返回 {platform_id: (region, native_currency)}；表不存在（低版本网关）⇒
    空表 ⇒ 全部观测计入 'unmapped'（不进任何地板：fail-closed，缺裁定就
    不分组，绝不按名字猜区域）。
    """
    sql = (
        "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tAc \""
        "SELECT platform_id, region, native_currency FROM market_platform_registry\""
    )
    try:
        out = ssh_query(host, sql)
    except subprocess.CalledProcessError:
        return {}
    reg = {}
    for line in out.splitlines():
        parts = [p.strip() for p in line.split("|")]
        if len(parts) == 3 and parts[0]:
            reg[parts[0]] = (parts[1], parts[2])
    return reg


def fetch_market(host: str, reg: dict) -> dict:
    """双地板聚合：overseas（USD 原生）/ domestic（CNY 原生）按 registry 裁定分组。

    ★ 未登记平台（不在 registry）的观测不进任何地板（A-17 原则 2：不按名字
    猜区域；unmapped 行数如实计数上报）；综合 CNY 折算轴（cny_*，采集时点
    折算）保持全量观测（旧口径不变）。
    """
    reg_values = ", ".join(
        "('%s','%s','%s')" % (pid.replace("'", "''"), region.replace("'", "''"), native.replace("'", "''"))
        for pid, (region, native) in sorted(reg.items())
    ) if reg else "('','','')"
    sql = (
        "docker exec acc-pg17 psql -U postgres -d salesdecorate -tAc \""
        "WITH reg(platform_id, region, native_currency) AS (VALUES " + reg_values + "), "
        "j AS (SELECT o.raw_model_name, o.currency, o.cny_input, o.cny_output, "
        "o.input_price, o.output_price, o.run_date, "
        "coalesce(reg.region, 'unmapped') AS region "
        "FROM price_observation o "
        "LEFT JOIN reg ON lower(reg.platform_id) = lower(o.platform_id) "
        "WHERE (o.input_price IS NOT NULL OR o.output_price IS NOT NULL) AND o.raw_model_name <> '') "
        "SELECT raw_model_name, min(cny_input), min(cny_output), max(run_date), "
        "min(input_price) FILTER (WHERE region='overseas' AND currency='USD'), "
        "min(output_price) FILTER (WHERE region='overseas' AND currency='USD'), "
        "min(input_price) FILTER (WHERE region='domestic' AND currency='CNY'), "
        "min(output_price) FILTER (WHERE region='domestic' AND currency='CNY'), "
        "min(currency), count(*) FILTER (WHERE region='unmapped') "
        "FROM j GROUP BY 1\""
    )
    rows = []
    for line in ssh_query(host, sql).splitlines():
        parts = [p.strip() for p in line.split("|")]
        if len(parts) != 10 or not parts[0]:
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

    reg = fetch_registry(args.ssh)
    print(f"registry platforms loaded: {len(reg)} (A-17 双币裁定权威)")
    market = fetch_market(args.ssh, reg)
    ours = fetch_ours(args.ssh)
    print(f"market models with price: {len(market)}")
    print(f"our bindings with price : {sum(len(v) for v in ours.values())} ({len(ours)} normalized names)")

    both = sorted(set(market) & set(ours))
    print(f"normalized intersection : {len(both)}")
    lines = []
    unmapped_rows = 0
    for key in both:
        m = market[key]
        # 列序（fetch_market）：0 raw 1 cny_in 2 cny_out 3 asof 4 ov_in 5 ov_out
        #                     6 dm_in 7 dm_out 8 min_currency 9 unmapped_rows
        cny_in, cny_out, run_date = m[1], m[2], m[3]
        ov_in, ov_out, dm_in, dm_out = m[4], m[5], m[6], m[7]
        unmapped_rows += int(m[9] or 0)
        for o in ours[key]:
            # A-17 双地板：overseas（USD 原生）/ domestic（CNY 原生）+ 综合 CNY 折算轴。
            # 比值只对「我方实付币种 == 地板原生币」成立：
            #   USD 实付 vs 海外地板；CNY 实付 vs 综合地板（CNY 折算轴，跨区可比）。
            our_in, our_out, cur, mode = o[1], o[2], o[3], o[4]
            ratio = ""
            if cur == "USD" and ov_in not in ("", "None") and our_in not in ("", "None") and float(ov_in) > 0:
                ratio = f"{float(our_in) / float(ov_in):.2f}x"
            elif cur == "CNY" and cny_in not in ("", "None") and our_in not in ("", "None") and float(cny_in) > 0:
                ratio = f"{float(our_in) / float(cny_in):.2f}x"
            lines.append({
                "model": key, "billing_mode": mode, "our_currency": cur,
                "our_in": our_in, "our_out": our_out,
                "market_min_in_cny": cny_in, "market_min_out_cny": cny_out,
                "market_overseas_in_usd": ov_in, "market_overseas_out_usd": ov_out,
                "market_domestic_in_cny": dm_in, "market_domestic_out_cny": dm_out,
                "market_asof": run_date, "in_ratio_ours_over_floor": ratio,
            })
    if unmapped_rows:
        print(f"WARNING: {unmapped_rows} observation rows on platforms NOT in registry "
              f"(未登记平台不计入任何地板 —— A-17 拒绝族 platform_unmapped_region 同规)")
    for l in lines:
        print(" | ".join(f"{k}={v}" for k, v in l.items() if v != ""))
    if args.out:
        with io.open(args.out, "w", encoding="utf-8", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(lines[0].keys()) if lines else ["model"])
            w.writeheader()
            w.writerows(lines)
        print(f"written: {args.out}")
    # 判读（只陈述事实）：比值>1.5 = 超过 2026-11-03 档的 supplier_price_drift 阈值
    # （A-17：USD 实付对海外地板、CNY 实付对综合地板，各自成立才计）。
    flagged = [l for l in lines if l["in_ratio_ours_over_floor"] and float(l["in_ratio_ours_over_floor"].rstrip("x")) > 1.5]
    print(f"drift>1.5x (vs same-currency floor): {len(flagged)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
