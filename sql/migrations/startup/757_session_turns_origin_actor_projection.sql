-- 757: session_turns_with_current_month 补 origin_actor 投影（R79 续十六）
--
-- 缺陷：基表 session_turns 与 session_turns_hot 都有 origin_actor（attnum 99），
-- 但本视图的 SELECT 列表里没有它。两条生产 SQL 引用 t.origin_actor：
--
--   domains/sessionsummary/message_source_v2.go  v2SessionBodiesBaseQuery
--   domains/sessionsummary/message_source_digest.go sessionTurnDigestQuery
--
-- 两条设置路径都会踩：
--   main_pipeline.go:1184  NewV2SessionBodiesSource → SessionMetadataCloseHook
--   main_pipeline.go:1416  NewPerTurnDigestSource（sessions_v2_compression_read 默认 true）
--
-- 源码常量原文 PREPARE 实测：ERROR: column t.origin_actor does not exist
--
-- close hook 吞掉错误只 Warn 并 return nil，而 session_analysis_metadata 的
-- status='final' 实测 **0 行**（147,358 行全是 provisional）——即每次会话关闭都失败
-- 且完全静默。admin 读侧 ORDER BY (status='final') DESC 退回 provisional，
-- 属优雅降级而非功能不可用。
--
-- 本迁移是**纯增量**：只在 SELECT 列表末尾加一列，不改动任何既有列的顺序与表达式，
-- 因此所有不引用 origin_actor 的现有查询行为完全不变。
--
-- ## 两条踩坑记录（都写在这里，别删）
--
-- 1. **不要把 CREATE OR REPLACE VIEW 包进 DO $$ ... EXECUTE $ddl$...$ddl$; $$**。
--    实测（PG 17.10，事务内）：同一份 DDL 顶层执行后视图列数 53 → 55，
--    而包在 DO 块的 EXECUTE 里执行后列数**保持 54 不变，且不报任何错**——
--    **静默无效**。同事务内改成 `DROP VIEW ... CASCADE` + `CREATE VIEW` 则有效，
--    但 CASCADE 会级联删除依赖视图，风险不可接受。
--    所以本文件采用「顶层 CREATE OR REPLACE + 前后两个 DO 断言」的写法：
--    断言用 DO（DO 里的纯 SELECT 断言是有效的，只有 DDL 被吞）。
--
-- 2. **列数别自己数**。我第一次用正则数 view 定义得 53，真库 information_schema
--    权威值是 54（`SELECT hot.id,` 行首不是空白，被正则漏掉），于是迁移里的
--    硬编码 53 会与真机不符。现在改为**执行前动态取 old_cols**，不硬编码。

-- ── 前置断言：视图存在，且尚未投影 origin_actor ────────────────────────────
DO $pre$
DECLARE
    old_cols int;
BEGIN
    SELECT count(*) INTO old_cols
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name   = 'session_turns_with_current_month';

    IF old_cols = 0 THEN
        RAISE EXCEPTION 'view public.session_turns_with_current_month not found — abort';
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public'
                  AND table_name   = 'session_turns_with_current_month'
                  AND column_name  = 'origin_actor') THEN
        RAISE NOTICE 'origin_actor already projected — this migration is idempotent, still refreshing';
    END IF;
    RAISE NOTICE 'pre-check ok: % columns, no origin_actor yet', old_cols;
END
$pre$;

-- ── 顶层执行（必须在这里，见文件头踩坑 1）──────────────────────────────────
-- 2026-10-01 加固（2026-10-01 收口轮）：本文件上一版重建视图时丢掉了 526/640/713
-- 一路携带的 WITH (security_invoker = true)——live 实测 reloptions 为空即本处
-- 所致（fresh-install e2e 轮文档误归因 713，713:43 实带该选项）。session_turns/_hot 均启用
-- RLS，视图吞 invoker 会让非 owner 读角色绕过 policy；owner 路径 relforcerow
-- security=false 不受影响。按 627 先例就地 SET 回来（详见文件尾守卫）。
CREATE OR REPLACE VIEW public.session_turns_with_current_month
WITH (security_invoker = true)
AS
 SELECT hot.id,
    hot.session_id,
    hot.turn_no,
    hot.tenant_id,
    hot.request_id,
    hot.project_id,
    hot.namespace,
    hot.parent_request_id,
    hot.task_type,
    hot.ts,
    hot.submit_mode,
    hot.compression_applied,
    hot.compression_strategy,
    hot.compression_meta,
    hot.compression_tokens_saved,
    hot.injection_verdict,
    hot.output_verdict,
    hot.model,
    hot.provider,
    hot.credential_id,
    hot.prompt_tokens,
    hot.completion_tokens,
    hot.cache_read_tokens,
    hot.cache_write_tokens,
    hot.cost_usd,
    hot.latency_ms,
    hot.status_code,
    hot.success,
    hot.error_kind,
    hot.source_kind,
    hot.quality,
    hot.partition_date,
    hot.attachment_count,
    hot.attachment_total_bytes,
    hot.multimodal_types,
    hot.attempt_no,
    hot.tools,
    hot.title,
    hot.summary,
    hot.digest,
    hot.aggregate_applied_at,
    hot.t0_arrived_at,
    hot.t1_total_enqueued_at,
    hot.t2_total_dequeued_at,
    hot.t3_model_enqueued_at,
    hot.t4_model_dequeued_at,
    hot.t5_cred_enqueued_at,
    hot.t6_cred_dequeued_at,
    hot.t7_forward_start_at,
    hot.t8_response_start_at,
    hot.t9_response_end_at,
    hot.client_protocol,
    hot.upstream_protocol,
    hot.ir_metadata,
    hot.origin_actor   /* ← 本次修法新增 */
   FROM session_turns_hot hot
  WHERE NOT (EXISTS ( SELECT 1
           FROM session_turns archived
          WHERE archived.tenant_id::text = hot.tenant_id::text AND archived.request_id = hot.request_id))
UNION ALL
 SELECT session_turns.id,
    session_turns.session_id,
    session_turns.turn_no,
    session_turns.tenant_id,
    session_turns.request_id,
    session_turns.project_id,
    session_turns.namespace,
    session_turns.parent_request_id,
    session_turns.task_type,
    session_turns.ts,
    session_turns.submit_mode,
    session_turns.compression_applied,
    session_turns.compression_strategy,
    session_turns.compression_meta,
    session_turns.compression_tokens_saved,
    session_turns.injection_verdict,
    session_turns.output_verdict,
    session_turns.model,
    session_turns.provider,
    session_turns.credential_id,
    session_turns.prompt_tokens,
    session_turns.completion_tokens,
    session_turns.cache_read_tokens,
    session_turns.cache_write_tokens,
    session_turns.cost_usd,
    session_turns.latency_ms,
    session_turns.status_code,
    session_turns.success,
    session_turns.error_kind,
    session_turns.source_kind,
    session_turns.quality,
    session_turns.partition_date,
    session_turns.attachment_count,
    session_turns.attachment_total_bytes,
    session_turns.multimodal_types,
    session_turns.attempt_no,
    session_turns.tools,
    session_turns.title,
    session_turns.summary,
    session_turns.digest,
    session_turns.aggregate_applied_at,
    session_turns.t0_arrived_at,
    session_turns.t1_total_enqueued_at,
    session_turns.t2_total_dequeued_at,
    session_turns.t3_model_enqueued_at,
    session_turns.t4_model_dequeued_at,
    session_turns.t5_cred_enqueued_at,
    session_turns.t6_cred_dequeued_at,
    session_turns.t7_forward_start_at,
    session_turns.t8_response_start_at,
    session_turns.t9_response_end_at,
    session_turns.client_protocol,
    session_turns.upstream_protocol,
    session_turns.ir_metadata,
    session_turns.origin_actor   /* ← 本次修法新增 */
   FROM session_turns;

-- ── 后置断言：列数恰好 +1 且 origin_actor 存在；不满足即报错 ─────────────────
DO $post$
DECLARE
    old_cols int;
    new_cols int;
BEGIN
    SELECT count(*) INTO new_cols
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name   = 'session_turns_with_current_month';
    SELECT count(*) INTO old_cols
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name   = 'session_turns_with_current_month'
       AND column_name  = 'origin_actor';

    IF old_cols <> 1 THEN
        RAISE EXCEPTION 'origin_actor projected % time(s) after rewrite (expected 1)', old_cols;
    END IF;
    -- 2026-10-01 加固：上一版丢 security_invoker 的正是本文件的重建语句，
    -- 此守卫防同型回归（形态照抄 526 同名守卫）。
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE oid = 'public.session_turns_with_current_month'::regclass
          AND 'security_invoker=true' = ANY (reloptions)
    ) THEN
        RAISE EXCEPTION 'session_turns_with_current_month must use security_invoker=true';
    END IF;
    RAISE NOTICE 'post-check ok: % columns incl. origin_actor', new_cols;
END
$post$;
