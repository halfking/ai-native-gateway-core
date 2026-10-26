#!/usr/bin/env python3
"""§143.36 / P12：看板「活跃模型数 / 活跃供应商数」读面的**可重跑 A/B 验证**。

它回答两个问题（都是本轮的关键结论）：

  Q1 语义**逐值**等价吗？
     改前 = 两个标量子查询（各带一条 `… IS NOT NULL` 尾臂）
     改后 = 单遍（无尾臂）
     ⇒ 因为 `COUNT(DISTINCT x)` 按定义忽略 NULL ⇒ 尾臂是 no-op ⇒ 逐值相等。

  Q2 成本差多少？
     ⇒ **主判据取 buffers**（确定性量，不受宿主负载影响）
     ⇒ 时间只作辅证（实测组内极差 > 组间差 ⇒ 时间不可作主判据）。

⚠★ 本脚本**只读**：所有语句都是 SELECT（EXPLAIN (ANALYZE) 真的执行 SELECT，
  但 SELECT 不写库）⇒ 可以在生产上跑；**不产生任何写入**。

用法（生产，走既有只读护栏）：
    bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_14336_board_ab.sql 600

用法（本地真库）：
    docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway -f scripts/audit/verify_14336_board_ab.sql

⚠ 窗口与租户按需改（脚本顶部两个变量）；改完必须同时改 §143.36 的读数表。
"""
import os
import sys

# ── 三个形态的 SQL 模板 ────────────────────────────────────────────────
# 现状（改前）：两个标量子查询 ⇒ 派生表被独立扫描两遍
CURRENT = """
SELECT
  (SELECT COUNT(DISTINCT COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,'')))
     FROM {SRC} AS r
    WHERE {WHERE} AND COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,'')) IS NOT NULL),
  (SELECT COUNT(DISTINCT r.provider_id) FROM {SRC} AS r
    WHERE {WHERE} AND r.provider_id IS NOT NULL)
"""

# 窄投影（9 列）× 2 子查询 —— ★ 实测【零收益】：planner 已把未引用的投影列裁掉
NARROW = """
SELECT
  (SELECT COUNT(DISTINCT COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,'')))
     FROM {SRC_NARROW} AS r
    WHERE {WHERE} AND COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,'')) IS NOT NULL),
  (SELECT COUNT(DISTINCT r.provider_id) FROM {SRC_NARROW} AS r
    WHERE {WHERE} AND r.provider_id IS NOT NULL)
"""

# 改后：单遍 ⇒ 一次扫描同时算两个聚合（★ 生产实测 buffers 恰好减半）
SINGLE = """
SELECT COUNT(DISTINCT COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,''))) AS models,
       COUNT(DISTINCT r.provider_id) AS providers
  FROM {SRC_NARROW} AS r
 WHERE {WHERE}
"""

# ── 派生表（宽投影 = 现状；窄投影 = 9 列）──────────────────────────────
# ★ 宽投影的列表达式与仓内 db.SessionFamilyTurnsSourceSQL() 逐字一致（由
#   §143.36.3 的骨架序列判定保证）⇒ 这里的字面量是【快照】，
#   改仓内投影时**必须同步这里**（否则 A/B 测的不是现网形态）。
WIDE_LEG = """
(SELECT NULL::bigint AS id, t.request_id AS request_id, t.ts AS ts, t.tenant_id::text AS tenant_id,
        d.client_model AS client_model, t.model AS outbound_model, d.provider_id AS provider_id,
        d.quality_flags AS quality_flags, t.task_type AS task_type,
        t.origin_actor::character varying(255) AS origin_actor,
        (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success'
              WHEN t.error_kind IN ('rate_limit_exceeded','key_throttled') THEN 'rate_limited'
              WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
   FROM public.session_turns_hot t
   LEFT JOIN public.session_turn_details_hot d
     ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date
 UNION ALL
 SELECT NULL::bigint AS id, t.request_id AS request_id, t.ts AS ts, t.tenant_id::text AS tenant_id,
        d.client_model AS client_model, t.model AS outbound_model, d.provider_id AS provider_id,
        d.quality_flags AS quality_flags, t.task_type AS task_type,
        t.origin_actor::character varying(255) AS origin_actor,
        (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success'
              WHEN t.error_kind IN ('rate_limit_exceeded','key_throttled') THEN 'rate_limited'
              WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
   FROM public.session_turns t
   LEFT JOIN public.session_turn_details d
     ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)
"""

NARROW_LEG = """
(SELECT t.ts AS ts, t.tenant_id::text AS tenant_id,
        d.client_model AS client_model, t.model AS outbound_model, d.provider_id AS provider_id,
        d.quality_flags AS quality_flags, t.task_type AS task_type,
        t.origin_actor::character varying(255) AS origin_actor,
        (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success'
              WHEN t.error_kind IN ('rate_limit_exceeded','key_throttled') THEN 'rate_limited'
              WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
   FROM public.session_turns_hot t
   LEFT JOIN public.session_turn_details_hot d
     ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date
 UNION ALL
 SELECT t.ts AS ts, t.tenant_id::text AS tenant_id,
        d.client_model AS client_model, t.model AS outbound_model, d.provider_id AS provider_id,
        d.quality_flags AS quality_flags, t.task_type AS task_type,
        t.origin_actor::character varying(255) AS origin_actor,
        (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success'
              WHEN t.error_kind IN ('rate_limit_exceeded','key_throttled') THEN 'rate_limited'
              WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
   FROM public.session_turns t
   LEFT JOIN public.session_turn_details d
     ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)
"""

# ── 窗口与租户（按需改）─────────────────────────────────────────────────
WIN_START = "'2026-09-21 00:00:00+00'"   # 30 天窗
WIN_END   = "'2026-10-21 00:00:00+00'"
TENANT    = ""                            # 空 = 平台口径（无 tenant_id 过滤）★ 最重的变体

WHERE = (
    f"r.ts >= {WIN_START} AND r.ts < {WIN_END}"
    + (f" AND r.tenant_id = {TENANT}" if TENANT else "")
    + " AND (NOT COALESCE('probe' = ANY(r.quality_flags), FALSE)"
      " AND COALESCE(r.task_type, '') <> 'probe_triggered'"
      " AND COALESCE(r.origin_actor, '') NOT IN"
      " ('node-probe-worker','active-probe-worker','probe-service','credential-selfcheck-worker'))"
      " AND r.request_status IN ('success', 'failure', 'rate_limited')"
)


def main():
    out = []
    out.append("-- §143.36 / P12：看板读面单遍 vs 双扫 的可重跑 A/B（只读）")
    out.append("-- 主判据 = buffers（确定性量）；时间只作辅证。")
    out.append("SET statement_timeout = '300s';")
    out.append("SET lock_timeout = '5s';")
    out.append("")

    shapes = [
        ("current", CURRENT, WIDE_LEG, WIDE_LEG),
        ("narrow",  NARROW,  WIDE_LEG, NARROW_LEG),
        ("single",  SINGLE,  WIDE_LEG, NARROW_LEG),
    ]

    # ── 语义对照（各形态取一次结果值）────────────────────────────────
    out.append("-- ===== 语义等价对照：三个形态的结果值必须逐值相等 =====")
    for name, tpl, src, src_n in shapes:
        out.append(f"\\echo === SEMANTIC {name} ===")
        out.append(tpl.format(SRC=src, SRC_NARROW=src_n, WHERE=WHERE).strip() + ";")

    # ── 预热（不计）───────────────────────────────────────────────────
    out.append("-- ===== 预热（不计）=====")
    for name, tpl, src, src_n in shapes:
        out.append(f"\\echo === WARMUP {name} ===")
        out.append("EXPLAIN (ANALYZE, BUFFERS) " +
                   tpl.format(SRC=src, SRC_NARROW=src_n, WHERE=WHERE).strip() + ";")

    # ── 交错 ABAB×4 ──────────────────────────────────────────────────
    out.append("-- ===== 交错 ABAB×4（不是 AAAA 再 BBBB）=====")
    order = ["current", "narrow", "single"] * 4
    idx = {n: (t, s, sn) for n, t, s, sn in shapes}
    for i, name in enumerate(order):
        tpl, src, src_n = idx[name]
        out.append(f"\\echo === ROUND {i+1} {name} ===")
        out.append("EXPLAIN (ANALYZE, BUFFERS) " +
                   tpl.format(SRC=src, SRC_NARROW=src_n, WHERE=WHERE).strip() + ";")

    sys.stdout.write("\n".join(out) + "\n")


if __name__ == "__main__":
    main()
