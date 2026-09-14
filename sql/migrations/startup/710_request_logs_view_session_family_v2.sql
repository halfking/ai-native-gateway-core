-- ===========================================================================
-- File:          sql/migrations/startup/710_request_logs_view_session_family_v2.sql
-- Migration:     710
-- Database:      llm_gateway
-- Purpose:       存储优化方案 v2 S2「拼装还原」第一步（plan §3 D6 / §4）：
--                重建同名视图 public.request_logs_with_current_month，视图体
--                从 "request_logs_hot UNION ALL request_logs"（v1，680/700 链）
--                换为「session 家族拼装体」：
--
--                    session_turns_hot ─┐
--                    session_turns      ├─ 113 列会话投影（显式 CAST 到
--                                          v1 冻结列契约，缺源列 NULL 补位）
--                    UNION ALL
--                    v1 体（hot∪parent + customer_id/request_class/due_at/
--                    system_fingerprint/raw_model_name lateral，原样冻结）
--                    × 反连接守卫（request_id 已入 session_turns 的 v1 行
--                    不再输出 → 双写期不重复计数）
--
--                约 40 个读方（admin/logs、仪表盘、timeline、turns_tree、
--                drift scanner 等）经同名同列契约零改动切换到会话族读路径
--                （plan §2.2 分组）。历史与未入会话族的行（探针存量、
--                request_logs 独有窗口、无 request_id 行）仍由 v1 分支供数，
--                S4 停写 + 历史 TTL 退出后自然收敛为纯会话体。
--
-- 列映射契约（113 列，缺源 = NULL 补位，plan §9 风险行 3；下列行号对应
-- DO block 内 proj 文本的表达式顺序）：
--   直映        request_id/ts/tenant_id/end_user_id/canonical_id/
--               submit_mode→request_mode/prompt+completion+cache tokens/
--               cost_usd/latency_ms/success/error_kind/search_text/
--               identity_hash/virtual_client_id/stream_*/checksums/
--               egress_protocol/failure_*/preview×2/transform_summary/
--               cost_display/cost_currency/usage_source/is_auto_request/
--               task_type/work_type/task_type_chosen/credits_charged/
--               parent_request_id/compression_strategy/compression_meta/
--               upstream_finish_reason/upstream_status_code/tools→tool_calls/
--               client_endpoint/client_timeout/client_request_id/
--               routing_attempts/routing_summary/agent_name/agent_type/
--               canonical_model/t0..t9/attachment_count/is_final_success/
--               customer_id/system_fingerprint/raw_model_name
--   派生映射    outbound_model←model；total_tokens←NULLIF(p+c,0)；
--               stream_done_received←stream_done_sent（707 缺源映射镜像，
--               plan §4.1-②）；
--               request_status←success/status_code（success→'success'、
--               429→'rate_limited'、失败→'failure'；归因近似，登记）；
--               has_attachments←attachment_count；auto_decision←TEXT::jsonb
--               （写源同 v1 jsonb 内容，plan §4.1-②）；
--               gw_session_id←session_id（'sys:%' 合成会话输出 NULL，
--               保真 v1 无会话头流量的 NULL 语义，plan §3 D4）
--   数值文本列  application_id/api_key_id/credential_id：TEXT→bigint 正则
--               守卫 CAST（S1a 落库恒为数字串或空）
--   NULL 补位   id/client_model/provider_id/client_profile/virtual_ip/
--               api_key_owner_user（OwnerUser 落 sessions.owner_user（706），
--               session_turns 无该列，S3 原生改造读 sessions）/
--               virtual_mac/affinity_hit/transform_rule_id/gw_task_id/
--               api_key_prefix/owner_user/application_code/key_alias/
--               auto_profile/confidence_num/model_chosen/strategy_used/
--               compression_reason/outbound_msg_count/outbound_token_est/
--               outbound_msg_hashes/quality_flags/quality_fix_actions/
--               quality_score/stream_chunk_errors/stream_chunks_sent/
--               test_col/test_tab_indent/provider_model/attachments/
--               request_type/request_class/due_at
--
-- Status:        active
-- Idempotent:    YES（viewdef 含 session_turns 即跳过；session_turns 缺表
--                或 canonical 列数 ≠ 113 时保留 v1 体并 notice）
-- Dependencies:  session_turns + session_turns_hot（430+707，列集相等）；
--                v1 包装链 577/610/696/700 原样沿用（成为 v1 分支定义者）；
--                v1 分支 lateral 形态按基础包装实况条件化（冻结链缺 fp/raw
--                追加 4 列；动态重建链自带只追加 2 列——两形态恒为
--                基础交集+5 = 113 列，与 696/700 双形态守卫同款）。
--
-- Safety:
--   - CREATE OR REPLACE VIEW only（EXECUTE 动态组装）：无 DROP，读者不会
--     见到缺失关系；113 列名称/顺序/类型逐一保持（显式 CAST 保证 UNION
--     类型解析不漂移）。
--   - 反连接走 idx_session_turns_request / idx_session_turns_hot_request，
--     谓词下推后逐行索引探测，不改变现有查询计划形态。
--   - session_turns_hot 与 session_turns 列集相等（707 双侧应用 + 集合契约
--     promote，plan §4.1-④）；投影在两侧重复展开（包装链同款惯例）。
--
-- 编号注记：方案文档原编号 709 已被共享账本占用（2026-09-14 14:19 并行线
-- work_type route coverage，schema_migrations 裸 '709'），按 699→700 先例
-- 重编号为 710；双账本核查 710 空闲。方案 §4 的 S4/S5 编号（710/711）顺延
-- 由届时双账本重查决定。
--
-- Go 镜像体：db/request_logs_view_schema.go（sessionFamilyProjectionV2 +
-- canonicalV2DDL，启动自愈/升级同体）；等价性由
-- db/view_schema_v2_contract_test.go 校验（ensure ≡ 本文件 viewdef）。
--
-- Rollback Script: 710_request_logs_view_session_family_v2.down.sql（恢复
--   700 体；schema_migrations 行按 append-only 惯例保留）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v2_already   boolean;
  turns_exist  boolean;
  canon_cols   integer;
  base_has_fp  boolean;
  base_has_raw boolean;
  proj         text;
  names        text;
  append_cols  text;
  lateral_h    text;
  lateral_p    text;
  v2_ddl       text;
BEGIN
  -- 体形探测基于 pg_views 行内求值：视图缺失时 EXISTS 子查询零行，
  -- pg_get_viewdef 不会被求值（直接 '...'::regclass 会 42P01 炸掉探针）。
  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public'
      AND viewname = 'request_logs_with_current_month'
      AND pg_get_viewdef(schemaname || '.' || viewname, true) LIKE '%session_turns%'
  ) INTO v2_already;

  IF v2_already THEN
    RAISE NOTICE '710: request_logs_with_current_month already carries the session-family v2 body; nothing to do';
    RETURN;
  END IF;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = 'public' AND table_name = 'session_turns'
  ) INTO turns_exist;

  IF NOT turns_exist THEN
    -- 430 前的极简/陈旧库：保留 v1 体，启动期 db.ensure 链按库况兜底
    -- （710 不逼停部署通道）。
    RAISE NOTICE '710: public.session_turns missing; keeping v1 request_logs view body';
    RETURN;
  END IF;

  -- 列数契约守卫：会话分支固定 113 列，v1 分支 = 基础交集 + 5 追加列。
  -- canonical 列数 ≠ 113 即基础交集契约已漂移（正常库恒为 108/110+5），
  -- 此时强行 UNION 会 "each UNION query must have the same number of
  -- columns" 失败——保留 v1 体并 notice，由视图契约修复流程先归位。
  SELECT count(*) INTO canon_cols
  FROM information_schema.columns
  WHERE table_schema = 'public' AND table_name = 'request_logs_with_current_month';
  -- 视图缺失时 count=0：直通建 v2（包装链在场即按当前形态组装）。
  IF canon_cols > 0 AND canon_cols <> 113 THEN
    RAISE NOTICE '710: canonical column count % <> 113 (frozen contract drift); keeping v1 body', canon_cols;
    RETURN;
  END IF;

  -- v1 分支 lateral 形态条件化（696/700 同款守卫语义）：冻结链基础交集缺
  -- system_fingerprint/raw_model_name → lateral 追加 4 列；动态重建链自带 →
  -- 追加会重复列，只追加 request_class/due_at。
  SELECT
    EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'system_fingerprint'
    ),
    EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'raw_model_name'
    )
  INTO base_has_fp, base_has_raw;

  append_cols := 'source.request_class, source.due_at';
  lateral_h   := 'h.request_class, h.due_at';
  lateral_p   := 'p.request_class, p.due_at';
  IF NOT base_has_fp THEN
    append_cols := append_cols || ', source.system_fingerprint';
    lateral_h   := lateral_h || ', h.system_fingerprint';
    lateral_p   := lateral_p || ', p.system_fingerprint';
  END IF;
  IF NOT base_has_raw THEN
    append_cols := append_cols || ', source.raw_model_name';
    lateral_h   := lateral_h || ', h.raw_model_name';
    lateral_p   := lateral_p || ', p.raw_model_name';
  END IF;

  -- ── 会话分支 113 列投影（别名固定 t；hot/parent 两分支复用）────────────
  proj := $proj$
NULL::bigint AS id
        , t.request_id AS request_id
        , t.ts AS ts
        , t.tenant_id::text AS tenant_id
        , (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id
        , (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) AS api_key_id
        , t.end_user_id AS end_user_id
        , NULL::text AS client_model
        , t.model AS outbound_model
        , (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id
        , NULL::bigint AS provider_id
        , t.canonical_id AS canonical_id
        , NULL::text AS client_profile
        , t.submit_mode AS request_mode
        , t.prompt_tokens AS prompt_tokens
        , t.completion_tokens AS completion_tokens
        , NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens
        , t.cost_usd::numeric(14,8) AS cost_usd
        , t.latency_ms AS latency_ms
        , t.success AS success
        , t.error_kind AS error_kind
        , t.search_text AS search_text
        , t.cache_read_tokens AS cache_read_tokens
        , t.cache_write_tokens AS cache_write_tokens
        , t.identity_hash AS identity_hash
        , t.virtual_client_id AS virtual_client_id
        , NULL::text AS virtual_ip
        , NULL::text AS virtual_mac
        , NULL::boolean AS affinity_hit
        , t.stream_first_chunk_ms AS stream_first_chunk_ms
        , t.stream_chunk_count AS stream_chunk_count
        , t.stream_interrupted AS stream_interrupted
        , t.stream_done_sent AS stream_done_sent
        , t.request_checksum AS request_checksum
        , t.response_checksum AS response_checksum
        , NULL::text AS transform_rule_id
        , t.egress_protocol AS egress_protocol
        , t.failure_stage AS failure_stage
        , t.failure_detail_code AS failure_detail_code
        , t.request_preview AS request_preview
        , t.transform_summary AS transform_summary
        , t.response_preview AS response_preview
        , t.stream_done_sent AS stream_done_received
        , t.cost_display::numeric(14,8) AS cost_display
        , t.cost_currency AS cost_currency
        , t.usage_source AS usage_source
        , (CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END) AS gw_session_id
        , NULL::text AS gw_task_id
        , (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
        , NULL::text AS api_key_prefix
        , NULL::text AS owner_user
        , NULL::text AS application_code
        , NULL::text AS key_alias
        , NULL::text AS api_key_owner_user
        , t.is_auto_request AS is_auto_request
        , t.task_type AS task_type
        , NULL::text AS auto_profile
        , (CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END) AS auto_decision
        , t.auto_confidence::numeric(4,3) AS auto_confidence
        , t.work_type AS work_type
        , t.task_type_chosen AS task_type_chosen
        , NULL::numeric(4,3) AS confidence_num
        , NULL::text AS model_chosen
        , NULL::text AS strategy_used
        , t.credits_charged AS credits_charged
        , t.parent_request_id AS parent_request_id
        , NULL::text AS compression_reason
        , t.compression_strategy AS compression_strategy
        , t.compression_meta AS compression_meta
        , NULL::integer AS outbound_msg_count
        , NULL::integer AS outbound_token_est
        , NULL::jsonb AS outbound_msg_hashes
        , NULL::text[] AS quality_flags
        , NULL::jsonb AS quality_fix_actions
        , NULL::numeric(3,2) AS quality_score
        , t.upstream_finish_reason AS upstream_finish_reason
        , t.tools AS tool_calls
        , t.client_endpoint AS client_endpoint
        , t.client_timeout AS client_timeout
        , NULL::integer AS stream_chunk_errors
        , NULL::integer AS stream_chunks_sent
        , t.client_request_id AS client_request_id
        , t.upstream_status_code AS upstream_status_code
        , NULL::text[] AS test_col
        , NULL::text AS test_tab_indent
        , NULL::text AS provider_model
        , NULL::jsonb AS attachments
        , (CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END) AS has_attachments
        , t.attachment_count AS attachment_count
        , t.routing_attempts AS routing_attempts
        , t.routing_summary AS routing_summary
        , t.agent_name AS agent_name
        , t.agent_type AS agent_type
        , t.client_protocol::character varying(50) AS client_protocol
        , t.canonical_model AS canonical_model
        , t.t0_arrived_at AS t0_arrived_at
        , t.t1_total_enqueued_at AS t1_total_enqueued_at
        , t.t2_total_dequeued_at AS t2_total_dequeued_at
        , t.t3_model_enqueued_at AS t3_model_enqueued_at
        , t.t4_model_dequeued_at AS t4_model_dequeued_at
        , t.t5_cred_enqueued_at AS t5_cred_enqueued_at
        , t.t6_cred_dequeued_at AS t6_cred_dequeued_at
        , t.t7_forward_start_at AS t7_forward_start_at
        , t.t8_response_start_at AS t8_response_start_at
        , t.t9_response_end_at AS t9_response_end_at
        , NULL::text AS request_type
        , t.is_final_success AS is_final_success
        , t.origin_actor::character varying(255) AS origin_actor
        , t.customer_id AS customer_id
        , NULL::text AS request_class
        , NULL::timestamptz AS due_at
        , t.system_fingerprint AS system_fingerprint
        , t.raw_model_name AS raw_model_name
$proj$;

  -- ── 113 列契约顺序（UNION ALL 按位置匹型；v1 分支内层在冻结/动态两形态
  -- 下列序不同——动态链 fp/raw 位于 customer/class/due 之前——外层按名
  -- 归一化后与会话分支对齐；两形态内层均恰好携带全部 113 个名字）────────
  names := $names$id, request_id, ts, tenant_id, application_id, api_key_id, end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id, client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens, identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id, egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview, stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id, request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user, is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type, task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint, client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col, test_tab_indent, provider_model, attachments, has_attachments, attachment_count, routing_attempts, routing_summary, agent_name, agent_type, client_protocol, canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type, is_final_success, origin_actor, customer_id, request_class, due_at, system_fingerprint, raw_model_name$names$;

  v2_ddl := format($ddl$
    CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
    SELECT %s
    FROM public.session_turns_hot t
    UNION ALL
    SELECT %s
    FROM public.session_turns t
    UNION ALL
    SELECT %s
    FROM (
      SELECT v.*, %s
      FROM public.request_logs_with_current_month_without_request_class_due_at v
      LEFT JOIN LATERAL (
          SELECT %s
          FROM public.request_logs_hot h
          WHERE h.request_id = v.request_id AND h.ts = v.ts
          UNION ALL
          SELECT %s
          FROM public.request_logs p
          WHERE p.request_id = v.request_id AND p.ts = v.ts
          LIMIT 1
      ) source ON true
    ) rl
    WHERE NOT EXISTS (SELECT 1 FROM public.session_turns_hot th WHERE th.request_id = rl.request_id)
      AND NOT EXISTS (SELECT 1 FROM public.session_turns tp WHERE tp.request_id = rl.request_id)
  $ddl$, proj, proj, names, append_cols, lateral_h, lateral_p);

  EXECUTE v2_ddl;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    '存储优化方案 v2 S2 拼装体（710）: session_turns(_hot) 113 列会话投影 '
    'UNION ALL v1 体（hot∪parent + 577/610/696/700 lateral，冻结）× 反连接 '
    '（request_id 已入 turns 的 v1 行不再输出）。缺源列 NULL 补位、派生映射与 '
    'sys:% 合成会话 NULL 语义登记见迁移文件列映射契约。S4 停写 + 历史 TTL '
    '退出后收敛为纯会话体。';

END $$;

-- Ledger self-registration: the upgrade channel only stamps
-- gateway_db_revision_sequences; schema_migrations rows exist only for files
-- that insert their own (698/699 precedent). Runs even on the no-op guard
-- path so the dual-ledger reconciliation stays aligned.
INSERT INTO public.schema_migrations (version, description)
VALUES ('710', 'storage plan v2 S2: request_logs_with_current_month rebuilt as session-family assembly (turns hot+parent projection UNION ALL frozen v1 body with anti-join dedup; renumbered 709->710, shared-ledger collision)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
