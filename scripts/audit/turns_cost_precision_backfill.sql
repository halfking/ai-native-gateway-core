-- turns_cost_precision_backfill.sql — 迁移 711 配套数据修补（E5 闭环）
--
-- 前置：711_session_turns_cost_precision.sql 已应用
-- （session_turns.cost_usd numeric(12,6) → numeric(14,8)）。
--
-- 把双写期已有 turns 行的 cost_usd 从 v1 终态行（父表∪hot 双侧）拷回 8 位
-- 精度值——此前写入时被 numeric(12,6) 舍入（E5：0.00001870 → 0.000019）。
-- SQL 侧 numeric 直拷，零浮点参与，精确对齐 v1。
--
-- request_logs_hot 是独立热表（非分区成员，341），行经 promote 移入父表
-- 月分区；同一 request_id 理论上只存在一侧，DISTINCT ON 兜底防 promote 竞态。
-- 幂等：差异才 UPDATE，可重复执行。
-- 用法：psql "$LLM_GATEWAY_DSN" -f scripts/audit/turns_cost_precision_backfill.sql

WITH v1cost AS (
    SELECT DISTINCT ON (request_id) request_id, cost_usd
    FROM (
        SELECT request_id, cost_usd FROM public.request_logs_hot
        WHERE request_id IS NOT NULL AND cost_usd IS NOT NULL
        UNION ALL
        SELECT request_id, cost_usd FROM public.request_logs
        WHERE request_id IS NOT NULL AND cost_usd IS NOT NULL
    ) u
    ORDER BY request_id
),
drift AS (
    SELECT t.id, v.cost_usd
    FROM public.session_turns t
    JOIN v1cost v ON v.request_id = t.request_id
    WHERE t.cost_usd IS DISTINCT FROM v.cost_usd
)
UPDATE public.session_turns t
SET cost_usd = d.cost_usd
FROM drift d
WHERE t.id = d.id;

-- 复验：双写期 cost 漂移应为 0
SELECT 'remaining_drift' AS k, count(*)::text AS v
FROM public.session_turns t
JOIN (
    SELECT DISTINCT ON (request_id) request_id, cost_usd
    FROM (
        SELECT request_id, cost_usd FROM public.request_logs_hot
        WHERE request_id IS NOT NULL AND cost_usd IS NOT NULL
        UNION ALL
        SELECT request_id, cost_usd FROM public.request_logs
        WHERE request_id IS NOT NULL AND cost_usd IS NOT NULL
    ) u
    ORDER BY request_id
) v ON v.request_id = t.request_id
WHERE t.cost_usd IS DISTINCT FROM v.cost_usd;
