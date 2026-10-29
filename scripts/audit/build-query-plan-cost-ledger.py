#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R180 轮：生成【生产语句代价台账】（query-plan-cost-ledger.tsv）。

★★ 为什么要有这张台账（★ 本轮最核心的一处认知修正）★★★

★ R179 轮建的「实测计划形态」轴答的是：**规划器实际走了哪条路**。
★★ 但用户原始目标里那条是：**「不要有表扫描或慢 SQL 的问题」**。
★★★ ⇒ 而「形态」**答不了「贵不贵」**：★★
  ★ 实测（252，104 条生产语句）：
  ★   · 形态 = seek（真走索引）的 6 条 ⇒ 累计只有 **52 秒**（★ 占 0.37%）
  ★   · 形态 = error（★ **测不出来**）的 72 条 ⇒ 累计 **10,987 秒（3.05 小时，★ 占 79%）**
  ★   · 累计代价 Top 1 那条（★ 145 分钟 / 21240 次）恰恰落在 error 里
★ ⇒ ⇒ **「形态」与「代价」几乎正交** ⇒ ⇒ 必须有【第二条轴】：★ **生产代价轴**。

★ 这条轴的数据**已经在事实源里**（★ pg_stat_statements 的 calls / total_exec_time_ms /
   mean_exec_time_ms / rows）⇒ ★★ R179 采了却**没进台账、没进门** ⇒ ⇒ 本轮把它接上。

★★ ★★ 一个关键的设计决定：**机器不猜「这条慢是谁的慢」★★

★ 本轮先试过「语句同时读 canonical 视图与底表 ⇒ 是审计对拍语句」⇒ ⇒ **实测被证伪**：
★   · 累计代价 Top 1 那条（★ 路由策略读点）也被判成审计 ⇒ ★★ 它其实是**应用读点**
★   · `INSERT INTO provider_metrics_minute`（★ rollup 写入）也被判成审计 ⇒ ★★ 它是**写入路径**
★ ⇒ ⇒ **「同时读两张表」这个特征不成立** ⇒ ⇒ 改口径：★★
★   · kind 只有两态：**app**（★ 应用读点）· **non-app**（★ 工具自己造成的慢）
★   · **non-app 由人工登记**（★ 在 query-plan-cost-exemptions.tsv 里，★ category=non-app）
★   · 生成器读那份登记 ⇒ non-app 的语句**不参与阈值判定**（★ over_threshold 写空）
★ ★★ 而**防「逃生舱」靠门**：★★
★   · 慢的 app 语句 ⇒ 必须有 category=slow 的登记（★ 写清为什么慢）
★   · non-app ⇒ 必须有 category=non-app 的登记（★ 写清它是审计/排障语句、来源在哪）
★   · ★ 两者互斥 ⇒ ⇒ **谁想把慢语句标成 non-app，就必须写下一条会被复核的理由**。

★★ 判定口径（★ 阈值都带来源，★ 不拍脑袋）★★★

  ① 单次慢：★ mean_ms >= 2000（★ **2 秒 = 用户原话**「对于超过 2 秒打开的网页，
     需要继续检查其慢的原因」）⇒ ⇒ 该语句必须挂【代价豁免】登记。
  ② 累计慢：★ total_ms >= 300000（★ 累计 5 分钟）⇒ ⇒ 同样必须挂登记
     （★ 这条抓的是【高频但单次不慢】的读点 —— 形态轴与单次耗时都抓不到它）。

★★ 输出列（★ 8 列）★★
  shape_id  kind  calls  total_ms  mean_ms  rows  plan_form  over_threshold
"""

import argparse
import csv
import hashlib
import os
import sys

# ── 阈值（★ 唯一的两条硬阈值；★ 改动必须同步改审计档里的读数） ──
THRESH_MEAN_MS = 2000.0      # ★ 用户原话：超过 2 秒
THRESH_TOTAL_MS = 300000.0   # ★ 累计 5 分钟（★ 高频慢读点）

KIND_APP = "app"
KIND_NONAPP = "non-app"

CATEGORY_SLOW = "slow"
CATEGORY_NONAPP = "non-app"


def die(msg: str) -> None:
    print("★ " + msg, file=sys.stderr)
    sys.exit(1)


def read_rows(path: str) -> list:
    """读 TSV，★ 显式跳过 '#' 注释行（★ R179 实测踩过：表头说明行恰好有制表符 ⇒ 被当成数据行）。"""
    out = []
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line.strip():
                continue
            if line.startswith("#"):
                continue
            out.append((lineno, line.split("\t")))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--statements", required=True, help="★ 生产语句事实源（★ canonical-view-statements.tsv）")
    ap.add_argument("--observations", required=True, help="★ 实测形态观测台账（★ query-plan-observations.tsv）")
    ap.add_argument("--exemptions", required=True, help="★ 代价豁免登记（★ query-plan-cost-exemptions.tsv）")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    if os.path.getsize(args.statements) == 0:
        die("生产语句事实源是空的 ⇒ ⇒ 事实源没了 ⇒ ⇒ 判据失效 ⇒ ⇒ 退出码非 0")

    obs = {}
    for _lineno, parts in read_rows(args.observations):
        if len(parts) < 3:
            continue
        obs[parts[0].strip()] = parts[2].strip()

    # ★ 豁免登记（★ 人工维护）⇒ ★ 决定「谁不参与阈值判定」。
    nonapp = set()
    slow = set()
    for _lineno, parts in read_rows(args.exemptions):
        if len(parts) < 3:
            continue
        sid = parts[0].strip()
        cat = parts[1].strip()
        if cat == CATEGORY_NONAPP:
            nonapp.add(sid)
        elif cat == CATEGORY_SLOW:
            slow.add(sid)
        else:
            die("豁免登记 %s 的 category=%r 不在 {slow, non-app} 里" % (sid, cat))

    rows = []
    seen = set()
    for lineno, parts in read_rows(args.statements):
        if len(parts) < 5:
            print(f"★ 跳过坏行 {lineno}", file=sys.stderr)
            continue
        calls, total_ms, mean_ms, rows_n, sql = parts[0], parts[1], parts[2], parts[3], parts[4]
        sql = sql.strip()
        if not sql:
            continue
        shape_id = "stmt-" + hashlib.sha256(sql.encode("utf-8")).hexdigest()[:10]
        if shape_id in seen:
            continue
        seen.add(shape_id)
        t = float(total_ms)
        m = float(mean_ms)
        kind = KIND_NONAPP if shape_id in nonapp else KIND_APP
        over = ""
        if kind == KIND_APP:
            if m >= THRESH_MEAN_MS:
                over = "mean>=2s"
            elif t >= THRESH_TOTAL_MS:
                over = "total>=5min"
        rows.append([
            shape_id,
            kind,
            calls,
            "%.1f" % t,
            "%.3f" % m,
            rows_n,
            obs.get(shape_id, "unmeasured"),
            over,
        ])

    # ★ 排序：★ 先按 kind，再按累计代价降序（★ 人读友好 + ★ 差集可比对）
    rows.sort(key=lambda r: (0 if r[7] else 1, r[1] != KIND_APP, -float(r[3])))

    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write("# query-plan-cost-ledger.tsv —— R180 轮：**生产语句代价台账**\n")
        fh.write("# ★ 生成器：scripts/audit/build-query-plan-cost-ledger.py\n")
        fh.write("# ★ 列：shape_id\tkind\tcalls\ttotal_ms\tmean_ms\trows\tplan_form\tover_threshold\n")
        fh.write("# ★ 事实源：252 的 pg_stat_statements（★ calls / total_exec_time_ms / mean_exec_time_ms / rows）\n")
        fh.write("# ★ ★ kind：app（★ 应用读点）· non-app（★ 工具自己造成的慢 ⇒ ★ 不参与阈值判定）\n")
        fh.write("# ★ ★ ★ non-app **只能**由人工在 query-plan-cost-exemptions.tsv 里登记（★ category=non-app）⇒ ★ 生成器不猜\n")
        fh.write("# ★ 阈值：★ mean_ms >= %.0f（★ **2 秒 = 用户原话**）★ 或 ★ total_ms >= %.0f（★ 累计 5 分钟）\n"
                 % (THRESH_MEAN_MS, THRESH_TOTAL_MS))
        fh.write("# ★ ★ over_threshold 非空 ⇒ 该语句必须挂【代价豁免】登记（★ category=slow）⇒ 否则门红\n")
        fh.write("# ★ ★ plan_form 取自实测形态观测台账（★ error = 测不出来；★ ★ 【测不出来】≠ 不重要 ⇒ 它可能正是最贵的那批）\n")
        w = csv.writer(fh, delimiter="\t", lineterminator="\n", quoting=csv.QUOTE_NONE, escapechar=None)
        for r in rows:
            w.writerow(r)

    over_n = sum(1 for r in rows if r[7])
    nonapp_n = sum(1 for r in rows if r[1] == KIND_NONAPP)
    total_s = sum(float(r[3]) for r in rows) / 1000.0
    app_s = sum(float(r[3]) for r in rows if r[1] == KIND_APP) / 1000.0
    print("★ 代价台账 %s：共 %d 行（★ app %d / non-app %d）⇒ ★ 超阈值 %d 条 ⇒ 累计 %.1f 秒（★ 其中 app 侧 %.1f 秒）"
          % (args.out, len(rows), len(rows) - nonapp_n, nonapp_n, over_n, total_s, app_s))
    if len(slow) != over_n:
        print("★ ★ 提示：豁免登记里的 slow 条数 = %d，★ 而台账算出的超阈条数 = %d ⇒ ★ 两者应一致（★ 不一致 ⇒ 门会红）"
              % (len(slow), over_n), file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
