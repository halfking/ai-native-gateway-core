# -*- coding: utf-8 -*-
#!/usr/bin/env python3
"""build-query-index-requirements.py —— R179 轮：把【读点】接进 query-index 台账。

# 输入
① scripts/audit/asset/canonical-read-point-shapes.json（★ 源码侧读点形态册）
② scripts/audit/asset/canonical-view-statements.tsv（★ 生产真实语句，★ 含调用次数）
③ scripts/audit/asset/query-plan-observations.tsv（★ 实测计划形态）

# 输出
① scripts/audit/asset/query-index-requirements.tsv（★ 扩到 11 列）
② scripts/audit/asset/query-plan-exemptions.tsv（★ 「没有生产流量」的登记）

# ★★ 台账为什么多出【实测计划形态】三列

★ R162 那道门判的是**静态轴**：★ 「这个表的父级索引有没有覆盖我的过滤列/排序列」。
★★★ 而 R179 补的是**动态轴**：★ 「规划器实际走了哪条路」。
★★★ 二者**不是**一回事，★ 而且【静态轴对视图型读点结构性不适用】：
   ★ canonical 视图**自己没有索引**（★ 索引在 session_turns / session_turns_hot 上）
   ⇒ ⇒ 视图型读点的「覆盖度」只能靠**实测**回答 ⇒ ⇒ ⇒ 台账需要第三态
   `measured-only`（★ 静态不可判定 ⇒ ★ 以实测为准）。

# ★★ 列（11 列）

	query_id	source	table	filter_cols	order_cols	status	fresh_status	plan_shape	plan_form	plan_env	note

★ status 取值域扩到三个：
	covered       —— 静态轴判「有索引覆盖」（★ 底表读点）
	known-gap     —— 静态轴判「没有索引覆盖」（★ 已登记的缺口）
	measured-only —— ★ **静态轴结构性不适用**（★ 视图型读点）⇒ ★ 以实测为准
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import importlib.util

_spec = importlib.util.spec_from_file_location(
    "b", os.path.join(os.path.dirname(os.path.abspath(__file__)), "build-plan-form-spec.py")
)
b = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(b)

# ★ 计划形态的值域（★ 门会校验）
PLAN_FORMS = {"seek", "filter", "seq-scan", "no-scan", "no-traffic", "dynamic", "unmeasured", "error"}


def read_tsv(path: str) -> list[list[str]]:
    out = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if not line.strip() or line.startswith("#"):
                continue
            out.append(line.rstrip("\n").split("\t"))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--shapes-json", required=True)
    ap.add_argument("--existing", default="", help="★ 已有台账（★ 原有行要保留，★ 只补齐新列）")
    ap.add_argument("--statements", required=True)
    ap.add_argument("--observations", required=True)
    ap.add_argument("--out-requirements", required=True)
    ap.add_argument("--out-exemptions", required=True)
    args = ap.parse_args()

    with open(args.shapes_json, encoding="utf-8") as fh:
        src_shapes = json.load(fh).get("shapes", [])

    # ── 生产语句 ──
    statements = []
    for parts in read_tsv(args.statements):
        if len(parts) < 5:
            continue
        calls, total_ms, mean_ms, rows, sql = parts[0], parts[1], parts[2], parts[3], parts[4]
        sid = "stmt-" + hashlib.sha256(sql.strip().encode("utf-8")).hexdigest()[:10]
        statements.append({
            "id": sid, "calls": int(calls or 0), "mean_ms": mean_ms, "rows": rows, "sql": sql.strip(),
        })

    # ── 实测形态 ──
    observations: dict[tuple[str, str], dict] = {}
    for parts in read_tsv(args.observations):
        if len(parts) < 4:
            continue
        observations[(parts[0], parts[1])] = {
            "plan_form": parts[2],
            "index_names": parts[3] if len(parts) > 3 else "",
            "index_cond_cols": parts[5] if len(parts) > 5 else "",
            "filter_cols": parts[6] if len(parts) > 6 else "",
            "rows_removed": parts[7] if len(parts) > 7 else "",
            "plan_rows": parts[8] if len(parts) > 8 else "",
            "source": parts[10] if len(parts) > 10 else "",
            "note": parts[12] if len(parts) > 12 else "",
        }

    envs = sorted({env for (_, env) in observations})
    primary_env = "252" if "252" in envs else (envs[0] if envs else "")

    # ── 源码读点 → 生产语句 ──
    MIN_FRAG = 25
    rows_out = []
    exemptions = []
    for sh in src_shapes:
        src_sql = sh.get("sql") or ""
        frag = b._norm_frag(b._where_clause(src_sql))
        if len(frag) < MIN_FRAG:
            frag = b._norm_frag(src_sql)
        cands = []
        for st in statements:
            if len(frag) >= MIN_FRAG and frag in b._norm_frag(st["sql"]):
                cands.append(st)
        # ★ 同形多态：★ 一个源码形态可能对应多条生产语句（★ 例如按天分臂的 rollup）
        if cands:
            cands.sort(key=lambda s: -s["calls"])
            best = cands[0]
            forms = set()
            for st in cands:
                obs = observations.get((st["id"], primary_env))
                if obs:
                    forms.add(obs["plan_form"])
            plan_shape = best["id"]
            obs = observations.get((plan_shape, primary_env))
            plan_form = obs["plan_form"] if obs else "unmeasured"
            plan_env = primary_env
            note = (f"★ 生产语句 {len(cands)} 条匹配（★ 调用最多的 {best['calls']} 次，"
                    f"★ 平均 {best['mean_ms']} ms）；★ 实测形态={plan_form}"
                    + (f"；★ 各匹配语句形态不一致={sorted(forms)}" if len(forms) > 1 else ""))
            if plan_form == "error":
                exemptions.append((sh["shape_id"], f"★ 实测失败：{obs['note'] if obs else ''}"))
        else:
            plan_shape = ""
            plan_form = "no-traffic"
            plan_env = primary_env
            note = "★ 在 252 的 pg_stat_statements 窗口里【没有匹配到生产语句】⇒ ⇒ 计划形态未知"
            exemptions.append((sh["shape_id"], "★ 生产上无匹配流量（★ pg_stat_statements 窗口内没有该形态的语句）"))

        rows_out.append({
            "query_id": sh["shape_id"],
            "source": f"{sh['file']}:{sh['line']}",
            "table": "request_logs_with_current_month",
            "filter_cols": ",".join(sh.get("sql_filter_cols") or []),
            "order_cols": (sh.get("sql_order_cols") or "").replace(",", " "),
            "status": "measured-only",
            "fresh_status": "measured-only",
            "plan_shape": plan_shape,
            "plan_form": plan_form,
            "plan_env": plan_env,
            "note": note,
        })

    # ── 保留已有行（★ 只补齐新列）────────────────────────────────
    kept = []
    if args.existing:
        for parts in read_tsv(args.existing):
            if len(parts) < 8:
                continue
            # ★★ 旧 8 列 ⇒ 新 11 列：★ **note 要挪到最后**（★ 新列插在它前面）
            #   旧：query_id source table filter_cols order_cols status fresh_status note
            #   新：query_id source table filter_cols order_cols status fresh_status
            #       plan_shape plan_form plan_env note
            row = parts[:7] + ["", "unmeasured", "", parts[7] if len(parts) > 7 else ""]
            kept.append(row)
            # ★★ 原有行**没有实测形态** ⇒ ⇒ 门会要求豁免登记（★ 如实登记，★ 不许默认放行）
            exemptions.append((row[0],
                "★ R162 轮的【静态轴】登记（★ 本轮未对这条做实测）⇒ ⇒ 实测形态未知"))

    with open(args.out_requirements, "w", encoding="utf-8") as fh:
        fh.write("# query-index-requirements.tsv —— ★★「活查询 ⇒ 过滤/排序列 ⇒ 索引可用性」需求台账\n")
        fh.write("# ★ 生成器：scripts/audit/build-query-index-requirements.py（★ R179 轮扩列）\n")
        fh.write("# ★ 消费方：scripts/ursmcheck/query_index_coverage_gate_test.go（★ Go 门）\n")
        fh.write("# ★ 列：query_id\tsource\ttable\tfilter_cols\torder_cols\tstatus\tfresh_status\tplan_shape\tplan_form\tplan_env\tnote\n")
        for r in kept:
            fh.write("\t".join(r) + "\n")
        for r in rows_out:
            fh.write("\t".join([
                r["query_id"], r["source"], r["table"], r["filter_cols"], r["order_cols"],
                r["status"], r["fresh_status"], r["plan_shape"], r["plan_form"], r["plan_env"],
                r["note"].replace("\t", " "),
            ]) + "\n")

    with open(args.out_exemptions, "w", encoding="utf-8") as fh:
        fh.write("# query-plan-exemptions.tsv —— R179 轮：**没有实测计划形态**的读点登记\n")
        fh.write("# ★ 生成器：scripts/audit/build-query-index-requirements.py\n")
        fh.write("# ★ 列：query_id\treason\n")
        for qid, reason in exemptions:
            fh.write(f"{qid}\t{reason}\n")

    print(f"★ 台账行 = {len(kept)}（原有）+ {len(rows_out)}（读点）；豁免登记 = {len(exemptions)}")
    from collections import Counter
    c = Counter(r["plan_form"] for r in rows_out)
    for k, v in c.most_common():
        print(f"★   plan_form={k}: {v}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
