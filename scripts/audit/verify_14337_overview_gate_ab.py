#!/usr/bin/env python3
"""§143.37 / P20：看板概览计数「门控前后」的**可重跑 A/B 验证**。

它回答两个问题（都是本轮的关键结论）：

  Q1 门控后载荷**逐值**变了吗？
     改前 = 无条件跑日志回退 ⇒ 最终值 =
          (m==0 && lm>0 ? lm : m,  p==0 && lp>0 ? lp : p)
     改后 = 仅当 (m==0 || p==0) 才跑日志回退 ⇒ 最终值 =
          (m,p)（两者都非零时，**回退结果被逐字丢弃** ⇒ 跳过 = 同一组值）
     ⇒ 脚本用**真实数据**把两边的最终值并排列出，并给出 `value_equivalent` 标志。
     ⇒ ★ 判据不是「看起来一样」，是 SQL 里逐值比出来的 `t/f`。

  Q2 门控省掉了多少？
     ⇒ 每次被跳过的那一扫的 buffers / 执行时间（EXPLAIN (ANALYZE, BUFFERS)）。
     ⇒ ★ 主判据取 buffers（确定性量）；时间只作辅证。
     ⇒ 生产**总体**口径（412 次 / 21.8 GB / 1,108.7 s 库时间）见 runbook §143.37.3。

⚠★ 本脚本**只读**：全部是 SELECT（EXPLAIN (ANALYZE) 真的执行 SELECT，
  但 SELECT 不写库）⇒ 可以在生产上跑；**不产生任何写入**。

用法（生产，走既有只读护栏）：
    python3 scripts/audit/verify_14337_overview_gate_ab.py > /tmp/p14337_ab.sql
    bash ~/.mavis-tools/lgwp-prod.sh /tmp/p14337_ab.sql 600

用法（本地真库）：
    python3 scripts/audit/verify_14337_overview_gate_ab.py > /tmp/p14337_ab.sql
    docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway -f /tmp/p14337_ab.sql

⚠ 投影是【12 列子集快照】（不是仓内 115 列全投影）：
  §143.36.6 已实测「把 115 列裁成 9 列对 buffers **零影响**」（planner 会裁掉
  未引用的投影列）⇒ 用子集快照测**成本**是有效的；
  但**数值**只依赖 client_model / outbound_model / provider_id / ts / tenant_id
  ⇒ 语义对照不受投影宽度影响。
"""
import sys

# ── 会话族派生表（12 列子集快照；列表达式与仓内 db.SessionFamilyTurnsSourceSQL() 逐字一致）──
SRC = """
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

# ── 看板的三个窗口 × 两种租户口径 ─────────────────────────────────────
# ★ 窗口用 now() 相对值（可重跑）；仓内真实窗口是「UTC 日界对齐」
#   （boardPresetTimeRange：todayStart-(days-1)*24h → now）⇒ 相差不到 1 天。
WINDOWS = [1, 7, 30]
SCOPES = [("platform", ""), ("tenant-default", "default")]


def where_clause(days, tenant):
    w = (f"r.ts >= now() - interval '{days} days' AND r.ts < now()")
    if tenant:
        w += f" AND r.tenant_id = '{tenant}'"
    w += (
        " AND (NOT COALESCE('probe' = ANY(r.quality_flags), FALSE)"
        " AND COALESCE(r.task_type, '') <> 'probe_triggered'"
        " AND COALESCE(r.origin_actor, '') NOT IN"
        " ('node-probe-worker','active-probe-worker','probe-service','credential-selfcheck-worker'))"
        " AND r.request_status IN ('success', 'failure', 'rate_limited')"
    )
    return w


def minute_path_sql(days, tenant):
    """仓内 queryOverviewCountsMinute 的逐字形态（admin/dashboard_board_queries.go）。"""
    if tenant:
        return (
            "SELECT (SELECT COUNT(*) FROM api_keys WHERE tenant_id = '%s' AND enabled = TRUE) AS keys,\n"
            "         (SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute\n"
            "           WHERE dim_type = 'model' AND tenant_id = '%s'\n"
            "             AND bucket >= now() - interval '%d days' AND bucket < now()) AS models,\n"
            "         (SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute\n"
            "           WHERE dim_type = 'provider' AND tenant_id = '%s'\n"
            "             AND bucket >= now() - interval '%d days' AND bucket < now()) AS providers"
            % (tenant, tenant, days, tenant, days)
        )
    return (
        "SELECT (SELECT COUNT(*) FROM api_keys WHERE enabled = TRUE) AS keys,\n"
        "         (SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute\n"
        "           WHERE dim_type = 'model'\n"
        "             AND bucket >= now() - interval '%d days' AND bucket < now()) AS models,\n"
        "         (SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute\n"
        "           WHERE dim_type = 'provider'\n"
        "             AND bucket >= now() - interval '%d days' AND bucket < now()) AS providers"
        % (days, days)
    )


def log_fallback_sql(days, tenant):
    """仓内 fillOverviewCountsFromLogs 的**改后**形态（§143.36 已改成单遍）。"""
    return (
        "SELECT COUNT(DISTINCT COALESCE(NULLIF(r.client_model,''),NULLIF(r.outbound_model,''))) AS lm,\n"
        "       COUNT(DISTINCT r.provider_id) AS lp\n"
        "  FROM %s AS r\n"
        " WHERE %s" % (SRC.strip(), where_clause(days, tenant))
    )


def main():
    out = []
    out.append("-- §143.37 / P20：看板概览计数「门控前后」可重跑 A/B（只读）")
    out.append("-- Q1 语义：before/after 的最终值并排 + value_equivalent 标志")
    out.append("-- Q2 成本：被跳过的那一扫的 buffers / exec time")
    out.append("SET statement_timeout = '300s';")
    out.append("SET lock_timeout = '5s';")
    out.append("")

    for days in WINDOWS:
        for scope, tenant in SCOPES:
            mp = minute_path_sql(days, tenant)
            lf = log_fallback_sql(days, tenant)
            out.append("\\echo === COMBO days=%d scope=%s ===" % (days, scope))
            out.append(
                "WITH mp AS MATERIALIZED (\n%s\n),\n"
                "lf AS MATERIALIZED (\n%s\n)\n"
                "SELECT mp.keys,\n"
                "       mp.models   AS m_models,\n"
                "       mp.providers AS m_providers,\n"
                "       lf.lm, lf.lp,\n"
                "       (CASE WHEN mp.models=0 AND lf.lm>0 THEN lf.lm ELSE mp.models END)   AS before_models,\n"
                "       (CASE WHEN mp.providers=0 AND lf.lp>0 THEN lf.lp ELSE mp.providers END) AS before_providers,\n"
                "       mp.models   AS after_models,\n"
                "       mp.providers AS after_providers,\n"
                "       ((CASE WHEN mp.models=0 AND lf.lm>0 THEN lf.lm ELSE mp.models END)   = mp.models\n"
                "        AND (CASE WHEN mp.providers=0 AND lf.lp>0 THEN lf.lp ELSE mp.providers END) = mp.providers)\n"
                "         AS value_equivalent,\n"
                "       (mp.models<>0 AND mp.providers<>0) AS log_scan_skipped_by_gate\n"
                "  FROM mp, lf;" % (mp, lf)
            )
            out.append("")

    # ── 成本：被跳过的那一扫的 buffers / 时间（预热后测）────────────────
    out.append("-- ===== Q2 成本：被门控跳过的那一扫（预热 + 单次测量）=====")
    for days in WINDOWS:
        for scope, tenant in SCOPES:
            lf = log_fallback_sql(days, tenant)
            out.append("\\echo === COST days=%d scope=%s ===" % (days, scope))
            out.append("-- 预热（不计）")
            out.append("EXPLAIN (ANALYZE, BUFFERS) " + lf + ";")
            out.append("-- 计量")
            out.append("EXPLAIN (ANALYZE, BUFFERS) " + lf + ";")
            out.append("")

    sys.stdout.write("\n".join(out) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
