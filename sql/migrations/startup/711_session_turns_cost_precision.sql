-- Migration 711: session_turns.cost_usd numeric(12,6) → numeric(14,8).
-- 存储优化方案 v2 E5（2026-09-15 观察台账 Round 1b 首个真实 G1-cost 样本）：
-- turns 列 12,6 在双写期对 v1 request_logs.cost_usd numeric(14,8) 舍入
-- （0.00001870 → 0.000019），D7 计费等值对账出现恒定 1e-8~5e-7 量级漂移。
-- 列放宽到与 v1 同精度 14,8 后，双写期两侧可精确判等。
--
-- cost_display 不动：该列为 DOUBLE PRECISION（53 位尾数 ≈15-16 位十进制
-- 有效数字），无 numeric scale 舍入问题，E5 样本不涉及。
--
-- 视图依赖处理（PG 拒绝 ALTER 被视图引用的列类型）：
--   - request_logs_with_current_month（710 体，Go 契约源 canonicalV2DDL）：
--     DROP 后由 db.ensureRequestLogsCurrentMonthView（db/db.go，migrate 子
--     命令与网关启动均跑）幂等重建，SQL 不复制 710 体。
--   - session_turns_with_current_month（640 体，Go ensure 链不覆盖）：本
--     迁移 DROP 后按 640 同款体原地重建（单一来源标注；若未来 640 体变更
--     需同步本文件的重建段，或届时将该视图也纳入 Go ensure 自愈）。
--
-- 回填（把双写期已有 turns 行的 cost_usd 从 v1 终态行拷回 8 位精度值）
-- 不在迁移内执行：数据修补与 DDL 分离，按 plan §4 惯例走独立脚本
-- scripts/audit/turns_cost_precision_backfill.sql，应用侧核对差异行数后执行。
--
-- 分区表：父表 ALTER 自动级联全部分区（2026_07..2026_10 + default，
-- 本机实测 26.8 万行，重写秒级）。放大 precision/scale 为纯放宽，无值截断。

DROP VIEW IF EXISTS public.request_logs_with_current_month;
DROP VIEW IF EXISTS public.session_turns_with_current_month;

ALTER TABLE public.session_turns
    ALTER COLUMN cost_usd TYPE numeric(14, 8);

-- 重建 session_turns_with_current_month（640_session_turns_protocol_fields
-- 同款体：hot 反连接防双写期重复 + 父表 UNION ALL，65 列直投）。
CREATE OR REPLACE VIEW public.session_turns_with_current_month
WITH (security_invoker = true) AS
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary, digest,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at,
    client_protocol, upstream_protocol, ir_metadata
FROM public.session_turns_hot hot
WHERE NOT EXISTS (
    SELECT 1
    FROM public.session_turns archived
    WHERE archived.tenant_id = hot.tenant_id
      AND archived.request_id = hot.request_id
)
UNION ALL
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary, digest,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at,
    client_protocol, upstream_protocol, ir_metadata
FROM public.session_turns;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('711', 'storage plan v2 E5: session_turns.cost_usd numeric(12,6) -> numeric(14,8) aligning request_logs precision; turns view rebuilt in-place, request_logs view by db.ensure')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
