#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.25 本地真库实测：654 真身把 301,255 行搬进 columnar 月分区的真实耗时。

★ 与 §143.23 脚本的**关键差别**：本脚本**不再把 `USING columnar` 替换成 `USING heap`**。
  §143.23 的诚实边界是「本地只测了 heap 路径 ⇒ 生产 columnar 路径的耗时仍未量化」——
  那个边界的**成因已查明**：本地镜像**确实带 columnar 扩展**（`citus_columnar 13.3-1`
  在 `pg_available_extensions` 里），只是**没装** ⇒ `CREATE EXTENSION citus_columnar`
  之后 `columnar` access method 就在了（实测 `pg_am` 出现 columnar）。
  ⇒ 本脚本走的是**与生产同一条 columnar 代码路径**（PG 17.10 + citus 13.3-1，两边同版本）。

证据力设计：
  ★ 被测函数体由脚本**从仓内 654 迁移提取**（不是手抄）⇒ 杜绝「抄错版本」。
  ★ columnar 臂**逐字不改** 654（含 `USING columnar`）；heap 臂只替换这一处 ⇒ 单一变量。
  ★ A/B 交错（ABAB…）+ 报中位与组内极差 ⇒ 不给单点读数（§143.20 方法论）。
  ★ 数据用**生产的真实取值域**（43 个 credential_id / 12 个真实模型名 / 4 种 billing_mode）
    ⇒ 合成数据的熵与基数贴近生产 ⇒ columnar 压缩行为有代表性（边界仍要写明）。

用法：
  python3 scripts/audit/verify_columnar_bulk_write_local.py                # 默认 301255 行 × 3 轮
  python3 scripts/audit/verify_columnar_bulk_write_local.py --rows 30000 --reps 2
"""
import argparse
import os
import re
import statistics
import subprocess
import sys
import time

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MIG654 = os.path.join(REPO, "sql/migrations/startup/654_archive_credential_model_index_detach_drop.sql")
CONTAINER = "llm-gateway-pg"
PGUSER = "llm_gateway"
DB = "itgate_14325"

# 生产 17 列结构（只读取自 172.16.2.210/llm_gateway，列名/类型/顺序逐字对齐）
COLS = """    bucket        timestamptz NOT NULL,
    credential_id bigint       NOT NULL,
    raw_model     text         NOT NULL,
    canonical_id  integer,
    billing_mode  text,
    unit_price_in_per_1m  numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window        integer,
    success_rate          numeric(5,4),
    p95_latency_ms        integer,
    active_sessions       integer,
    concurrency_limit     integer,
    pressure_ratio        numeric(5,4),
    score_smart           numeric(8,4),
    score_speed_first     numeric(8,4),
    score_cost_first      numeric(8,4),
    updated_at            timestamptz"""

# 生产 archive 父表的 3 个 ON ONLY 索引（逐字对齐）
ARCH_IDX = """CREATE INDEX idx_cmi_archive_cred_model ON ONLY public.credential_model_index_archive USING btree (credential_id, raw_model, bucket DESC);
CREATE INDEX idx_cmi_archive_bucket     ON ONLY public.credential_model_index_archive USING btree (bucket DESC);
CREATE INDEX idx_cmi_archive_canonical  ON ONLY public.credential_model_index_archive USING btree (canonical_id, bucket DESC) WHERE (canonical_id IS NOT NULL);"""

# 生产源分区的 2 个索引（逐字对齐：1 UNIQUE + 1 btree）
SRC_IDX = """CREATE UNIQUE INDEX credential_model_index_2026_0_bucket_credential_id_raw_mode_idx ON public.credential_model_index_2026_09 USING btree (bucket, credential_id, raw_model);
CREATE INDEX credential_model_index_2026_0_credential_id_raw_model_buck_idx1 ON public.credential_model_index_2026_09 USING btree (credential_id, raw_model, bucket DESC);"""

# 生产的真实取值域（2026-10-20 只读取数）
MODELS = ("deepseek-v4-flash", "deepseek-v4-pro", "kimi-k3", "glm-5.2", "claude-opus-5",
           "gpt-5.6-terra", "gpt-5.6-sol", "claude-fable-5", "claude-opus-4-8",
           "claude-sonnet-5", "minimax-M3", "gpt-5.6-luna")
BILLING = ("per_token", "per_token", "per_token", "token_plan", "free")


def extract_654_body(am: str) -> str:
    """从仓内 654 迁移提取函数体；am='columnar' 时逐字不改，am='heap' 只替换这一处。"""
    with open(MIG654, encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"CREATE OR REPLACE FUNCTION public\.archive_credential_model_index\(archive_month date\).*?\$\$;",
                  text, re.S)
    if not m:
        print("从 654 迁移里提取函数体失败", file=sys.stderr)
        sys.exit(2)
    body = m.group(0)
    assert "USING columnar" in body, "654 原文里没有 USING columnar —— 提取到的不是预期的版本"
    if am == "heap":
        body = body.replace("USING columnar", "USING heap")
    return body


def psql(sql: str, db: str = "postgres", timing: bool = False) -> str:
    args = ["docker", "exec", "-i", CONTAINER, "psql", "-U", PGUSER, "-d", db,
            "-v", "ON_ERROR_STOP=0", "-P", "pager=off"]
    if timing:
        args += ["-c", r"\timing on"]
    args += ["-f", "-"]
    p = subprocess.run(args, input=sql, capture_output=True, text=True)
    return p.stdout + p.stderr


def timed(sql: str, db: str) -> tuple[float, str]:
    """跑一段 SQL 并返回 (psql 报的毫秒数, 原始输出)。"""
    out = psql(sql, db, timing=True)
    times = [float(x) for x in re.findall(r"^Time:\s+([0-9.]+)\s+ms", out, re.M)]
    return (times[-1] if times else float("nan")), out


def setup(rows: int) -> None:
    """建同构 schema + 源分区 + 合成数据（本地一次性库，退出即 DROP）。"""
    models = "ARRAY[" + ",".join("'%s'" % m for m in MODELS) + "]"
    billing = "ARRAY[" + ",".join("'%s'" % b for b in BILLING) + "]"
    sql = f"""
DROP DATABASE IF EXISTS {DB};
CREATE DATABASE {DB};
\\connect {DB}
CREATE EXTENSION IF NOT EXISTS citus_columnar;

\\echo '=== 建同构 schema（17 列 + 生产同款索引）==='
CREATE TABLE public.credential_model_index (
{COLS}
) PARTITION BY RANGE (bucket);

CREATE TABLE public.credential_model_index_archive (
{COLS}
) PARTITION BY RANGE (bucket);

{ARCH_IDX}

\\echo '=== 源分区 + 生产同款 2 个索引 ==='
CREATE TABLE public.credential_model_index_2026_09 PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
{SRC_IDX}

\\echo '=== 合成 {rows} 行（取值域取自生产）==='
INSERT INTO public.credential_model_index_2026_09
SELECT '2026-09-01 00:00:00+08'::timestamptz + ((i % 8640) * interval '5 minutes'),
       2 + ((i / 8640) % 43),
       ({models})[((i % 8640) + 2 + (i / 8640) % 43) % 12 + 1],
       1 + ((i::bigint * 7451) % 3117059)::integer,
       ({billing})[1 + (i % 5)],
       round((3.3718 + (i % 50) * 0.01)::numeric, 4),
       round((16.3823 + (i % 70) * 0.01)::numeric, 4),
       365062 + (i % 200000),
       round(((i % 100) / 100.0)::numeric, 4),
       5737 + (i % 9000),
       0,
       19 + (i % 30),
       0.0000,
       round((49.4305 + (i % 100) * 0.01)::numeric, 4),
       round((47.4212 + (i % 100) * 0.01)::numeric, 4),
       round((34.0005 + (i % 100) * 0.01)::numeric, 4),
       '2026-09-01 00:00:00+08'::timestamptz + ((i % 8640) * interval '5 minutes')
  FROM generate_series(0, {rows - 1}) i;

SELECT count(*) AS src_rows FROM public.credential_model_index_2026_09;
"""
    out = psql(sql)
    print(out)
    if "ERROR" in out:
        print("setup 失败", file=sys.stderr)
        sys.exit(2)


def rebuild_source(rows: int) -> None:
    """重跑前把源分区复原（654 会 DETACH + DROP 它）。"""
    models = "ARRAY[" + ",".join("'%s'" % m for m in MODELS) + "]"
    billing = "ARRAY[" + ",".join("'%s'" % b for b in BILLING) + "]"
    sql = f"""
DROP TABLE IF EXISTS public.credential_model_index_2026_09;
CREATE TABLE public.credential_model_index_2026_09 PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
{SRC_IDX}
INSERT INTO public.credential_model_index_2026_09
SELECT '2026-09-01 00:00:00+08'::timestamptz + ((i % 8640) * interval '5 minutes'),
       2 + ((i / 8640) % 43),
       ({models})[((i % 8640) + 2 + (i / 8640) % 43) % 12 + 1],
       1 + ((i::bigint * 7451) % 3117059)::integer,
       ({billing})[1 + (i % 5)],
       round((3.3718 + (i % 50) * 0.01)::numeric, 4),
       round((16.3823 + (i % 70) * 0.01)::numeric, 4),
       365062 + (i % 200000),
       round(((i % 100) / 100.0)::numeric, 4),
       5737 + (i % 9000),
       0,
       19 + (i % 30),
       0.0000,
       round((49.4305 + (i % 100) * 0.01)::numeric, 4),
       round((47.4212 + (i % 100) * 0.01)::numeric, 4),
       round((34.0005 + (i % 100) * 0.01)::numeric, 4),
       '2026-09-01 00:00:00+08'::timestamptz + ((i % 8640) * interval '5 minutes')
  FROM generate_series(0, {rows - 1}) i;
"""
    out = psql(sql, DB)
    if "ERROR" in out:
        print("rebuild 失败", file=sys.stderr)
        print(out, file=sys.stderr)
        sys.exit(2)


def run_archive(am: str, rows: int) -> tuple[float, str]:
    """装上 654（columnar 臂逐字 / heap 臂只替换 AM）并调用一次，返回耗时(ms)。"""
    body = extract_654_body(am)
    psql(body, DB)
    # 每臂从干净状态开始：dst 分区若已存在，654 会走 EXCEPT ALL 比对分支而不是 INSERT 分支
    # （那是幂等保护，工作正常；但本臂要测的是「首次真归档」的路径）。
    psql("DROP TABLE IF EXISTS public.credential_model_index_archive_2026_09;", DB)
    rebuild_source(rows)
    ms, out = timed("SELECT * FROM public.archive_credential_model_index('2026-09-03');", DB)
    print(out)
    return ms, out


def manual_breakdown(am: str, rows: int) -> dict:
    """不用函数，逐步计时 654 的等价动作（拆出 INSERT / DETACH / DROP / COMMIT）。"""
    psql(f"""
DROP TABLE IF EXISTS public.credential_model_index_archive_2026_09;
""", DB)
    rebuild_source(rows)
    sql = f"""
BEGIN;
CREATE TABLE public.credential_model_index_archive_2026_09 PARTITION OF public.credential_model_index_archive
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01') USING {am};
INSERT INTO public.credential_model_index_archive_2026_09 (bucket, credential_id, raw_model, canonical_id,
    billing_mode, unit_price_in_per_1m, unit_price_out_per_1m, context_window, success_rate,
    p95_latency_ms, active_sessions, concurrency_limit, pressure_ratio, score_smart,
    score_speed_first, score_cost_first, updated_at)
SELECT bucket, credential_id, raw_model, canonical_id, billing_mode, unit_price_in_per_1m,
    unit_price_out_per_1m, context_window, success_rate, p95_latency_ms, active_sessions,
    concurrency_limit, pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at
  FROM public.credential_model_index_2026_09;
SELECT count(*) AS moved_rows FROM public.credential_model_index_archive_2026_09;
SELECT pg_size_pretty(pg_total_relation_size('public.credential_model_index_2026_09')) AS src_sz,
       pg_size_pretty(pg_total_relation_size('public.credential_model_index_archive_2026_09')) AS dst_sz;
ALTER TABLE public.credential_model_index_archive DETACH PARTITION public.credential_model_index_archive_2026_09;
DROP TABLE public.credential_model_index_archive_2026_09;
COMMIT;
"""
    ms, out = timed(sql, DB)
    print(out)
    times = [float(x) for x in re.findall(r"^Time:\s+([0-9.]+)\s+ms", out, re.M)]
    # psql 会给 BEGIN 也打一条 timing ⇒ 标签序列必须与语句数一一对应
    labels = ["begin", "create_partition", "insert", "count", "detach", "drop", "commit"]
    d = {}
    for i, lab in enumerate(labels):
        if i < len(times):
            d[lab] = times[i]
    d["_total_ms"] = ms
    m = re.search(r"moved_rows\s*\n\s*-+\s*\n\s*(\d+)", out)
    d["_archived_rows"] = m.group(1) if m else "?"
    msz = re.search(r"src_sz\s*\|\s*dst_sz\s*\n[-\s|]+\n\s*(\S+)\s*\|\s*(\S+)", out)
    d["_sizes"] = f"src {msz.group(1)} / dst {msz.group(2)}" if msz else "?"
    return d


def summarize(name: str, vals: list[float]) -> str:
    good = [v for v in vals if v == v]  # 去掉 NaN
    if not good:
        return f"{name}: 无有效读数"
    med = statistics.median(good)
    lo, hi = min(good), max(good)
    return (f"{name}: n={len(good)} 中位={med:,.0f} ms  极差=[{lo:,.0f}, {hi:,.0f}] ms  "
            f"（组内极差 {hi - lo:,.0f} ms = {(hi - lo) / med * 100:.1f}%）")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rows", type=int, default=301255)
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--scale", action="store_true", help="额外跑 30k/100k 两个规模看 a+bN")
    args = ap.parse_args()

    print(f"★ 目标行数 = {args.rows:,}（生产 09 月分区的实际行数）")
    print(f"★ 654 函数体从仓内迁移提取；columnar 臂**逐字不改**，heap 臂只替换 USING columnar→USING heap\n")

    setup(args.rows)

    # ── A/B 交错：columnar 与 heap 两臂，ABAB… 顺序，各 reps 轮
    col_vals: list[float] = []
    heap_vals: list[float] = []
    for rep in range(args.reps):
        for arm, bucket in (("columnar", col_vals), ("heap", heap_vals)):
            ms, out = run_archive(arm, args.rows)
            bucket.append(ms)
            print(f"--- rep{rep} {arm}: {ms:,.0f} ms ---")
            if "ERROR" in out:
                print("★ 该臂报错，停止", file=sys.stderr)
                psql(f"DROP DATABASE IF EXISTS {DB};")
                return 2

    print()
    print("════ A/B 结果（交错顺序，报中位与组内极差）════")
    print(summarize("columnar 臂（生产同款路径）", col_vals))
    print(summarize("heap 臂（对照，仅换 AM）", heap_vals))
    if col_vals and heap_vals:
        c, h = statistics.median(col_vals), statistics.median(heap_vals)
        print(f"⇒ columnar 相对 heap 的成本倍数 = {c / h:.2f}×（中位口径）")

    # ── 逐步拆解（一次即可，看时间花在哪一步）
    print("\n════ 逐步拆解（654 等价动作，columnar）════")
    bd = manual_breakdown("columnar", args.rows)
    for k in ("create_partition", "insert", "detach", "drop", "commit"):
        if k in bd:
            print(f"  {k:<16} {bd[k]:>12,.0f} ms")
    print(f"  {'合计':<16} {bd['_total_ms']:>12,.0f} ms")
    print(f"  搬过去的行数 = {bd['_archived_rows']}")
    print(f"  体积对照（源 / dst）：{bd.get('_sizes', '?')}   （生产实测：源 credential_model_index_2026_09 = 84 MB）")
    cp_ms, cp_out = timed("CHECKPOINT;", DB)
    print(f"  随后的 CHECKPOINT（columnar 刷盘/压缩的延迟成本）= {cp_ms:,.0f} ms")

    # ── 缩放曲线（可选）
    if args.scale:
        print("\n════ 缩放曲线（columnar 臂）════")
        for n in (30000, 100000):
            vals = []
            for _ in range(2):
                ms, _out = run_archive("columnar", n)
                vals.append(ms)
            print(summarize(f"n={n:,}", vals))

    print("\n════ 与 10 分钟预算（runArchive 的 context timeout = 600,000 ms）对比 ════")
    if col_vals:
        c = statistics.median(col_vals)
        print(f"  columnar 中位耗时 {c:,.0f} ms  ⇒ 预算占用 {c / 600000 * 100:.1f}%  "
              f"（余量 {600000 - c:,.0f} ms）")
        if c > 600000:
            print("  ★ 结论：**超过 10 分钟超时** ⇒ 11-03 会超时回滚")
        else:
            print("  ★ 结论：在本地量级下**未超过** 10 分钟超时（但这不是生产承诺，见 runbook 边界）")

    psql(f"DROP DATABASE IF EXISTS {DB};")
    print(f"\n（一次性库 {DB} 已 DROP）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
