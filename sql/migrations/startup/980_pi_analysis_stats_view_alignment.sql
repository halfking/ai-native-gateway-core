-- Migration: 980_pi_analysis_stats_view_alignment
-- Purpose: v_error_analysis_stats 对齐 GetStats 新口径（补 processing/failed/skipped 计数）
-- Phase: PI 分析统计一致性批判审计轮（2026-11-19）
-- Author: System
-- Date: 2026-11-19
--
-- ★ 背景（2026-11-19 批判式审计发现 F1）：
--     统计卡与列表一致性根修（提交 24d352f17）把 Go 侧 GetStats 改成
--   「全量域 + 全部 5 状态」口径，但 977 订正后的 v_error_analysis_stats
--   视图仍是旧口径——只有 analyzed_count/pending_count，**没有
--   processing/failed/skipped**。该视图当前零运行时消费者（grep 全仓
--   只命中迁移/测试/注释），但它是 DB 里的**公开真值**：未来任何监控
--   探针、DBA 手查、或新代码接线都会拿到与页面统计卡**不同的口径**
--   （例：211 队列 2 条全 failed ⇒ 页面显示 失败=2，视图里这一维度
--   不存在 ⇒ 读视图的人会误判「没有失败」）。
--   ⇒ 本仓纪律：「同一份内容两份真值必然漂移」——视图对齐新口径，
--   保持 CREATE OR REPLACE 幂等可重放。
--
-- ★ 口径契约（与 error_collector.go GetStats 逐项一致）：
--     total_errors          = COUNT(DISTINCT queue_id)          —— 全量域（无时间过滤）
--     pending_count         = FILTER (status = 'pending')
--     processing_count      = FILTER (status = 'processing')     —— 本件新增
--     analyzed_count        = FILTER (status = 'analyzed')
--     failed_count          = FILTER (status = 'failed')         —— 本件新增
--     skipped_count         = FILTER (status = 'skipped')        —— 本件新增
--     false_positive_count  = FILTER (is_false_positive = TRUE)  —— 结果侧（LATERAL 每队列取最新一条）
--     avg_confidence / avg_analysis_time_ms / total_tokens_used / last_error_at —— 同 977
--   ★ 结果侧聚合（误报/置信/耗时/token）在视图里按 error_type 分组，
--     与 GetStats 的结果表全域聚合口径有分组差异（视图=按类型、Go=全局），
--     两者各自成立（Σ视图分组 = Go 全局），属披露口径而非漂移。
--
-- ★ 幂等：CREATE OR REPLACE VIEW + 台账 ON CONFLICT DO UPDATE
--   ⇒ 内容指纹重放零冲突。
--
-- ★ 视图在场守卫：视图由 971 建出、977 订正。「既没跑 971」的库上
--   裸 CREATE OR REPLACE 视图会因基表缺失而 42P01 ⇒ 判 to_regclass
--   后跳过（与 977/979 同款防御性口径）。

BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DO $$
BEGIN
    -- ★ 2026-11-19 二修（三台部署活体打回）：CREATE OR REPLACE VIEW 不允许
    --   改变既有列的位置（977 列序 analyzed_count 在 pending_count 之前，
    --   首版把顺序对调 ⇒ "cannot change name of view column"）。
    --   ⇒ 改用 DROP VIEW IF EXISTS + CREATE VIEW：允许任意列序，且仍幂等
    --   （整个 DO 块在迁移事务内，DROP 与 CREATE 之间失败会整体回滚，
    --   不会留下无视图的中间态）。
    IF to_regclass('public.v_error_analysis_stats') IS NULL THEN
        RAISE NOTICE '980: public.v_error_analysis_stats 不在场（971 未应用）⇒ 跳过视图口径对齐';
        RETURN;
    END IF;

    DROP VIEW IF EXISTS public.v_error_analysis_stats;

    CREATE VIEW public.v_error_analysis_stats AS
    WITH per_queue AS (
        SELECT
            q.id                     AS queue_id,
            q.error_type             AS error_type,
            q.status                 AS status,
            q.created_at             AS created_at,
            r.is_false_positive      AS is_false_positive,
            r.confidence_score       AS confidence_score,
            r.analysis_duration_ms   AS analysis_duration_ms,
            r.llm_tokens_used        AS llm_tokens_used
        FROM public.error_analysis_queue q
        LEFT JOIN LATERAL (
            SELECT
                rr.is_false_positive,
                rr.confidence_score,
                rr.analysis_duration_ms,
                rr.llm_tokens_used
            FROM public.error_analysis_results rr
            WHERE rr.queue_id = q.id
            ORDER BY rr.id DESC
            LIMIT 1
        ) r ON TRUE
    )
    SELECT
        error_type,
        COUNT(DISTINCT queue_id) AS total_errors,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'pending') AS pending_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'processing') AS processing_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'analyzed') AS analyzed_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'failed') AS failed_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE status = 'skipped') AS skipped_count,
        COUNT(DISTINCT queue_id) FILTER (WHERE is_false_positive = TRUE) AS false_positive_count,
        AVG(confidence_score) AS avg_confidence,
        AVG(analysis_duration_ms) AS avg_analysis_time_ms,
        SUM(llm_tokens_used) AS total_tokens_used,
        MAX(created_at) AS last_error_at
    FROM per_queue
    GROUP BY error_type;
END;
$$;

-- ★ 双账本纪律：与 971/977/979 同款（schema_migrations 本件自登记；
--   gateway_db_revision_sequences 由 deploy 通道按内容指纹写）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('980', 'pi_analysis_stats_view_alignment: v_error_analysis_stats 对齐 GetStats 新口径（补 processing/failed/skipped 计数）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
