-- ===========================================================================
-- File:          sql/migrations/startup/824_request_status_rate_limited_projection.down.sql
-- Migration:     824 down
-- Purpose:       把 request_status 的会话腿投影**退回 824 之前**的形态
--                （即只看 status_code=429 的那一版）。
--
-- 基准是 **817 的 up 文件本体**，不是 817 的 down：817 的 up 恰好就是
-- 「824 之前」的确切 viewdef（语义 client_ip 守卫 + 旧 request_status 表达式），
-- 逐字重建它即得确定性回退。拿 817 的 down 当基准会**顺带把 client_ip 守卫
-- 退回 816 的已知弱形态**（审计 §9.64）——那是另一场事故，不属于这次回滚。
--
-- 回滚是有代价的，请先读 §9.160：
--   退回后 437,402 条真限流会重新被报成普通 `failure`，视图把 failure
--   高估 32%、rate_limited 归零。所以这个 down 只在两种情况下是正确答案：
--     ① 装 824 之前把库退回旧状态（正常回滚路径）；
--     ② 需要复现旧行为来对照排查。
--   **不要**因为「824 让某个数字变难看了」就回滚——限流被算成上游失败才是
--   错的那个方向。
--
-- 列序/列数: 118 → 118（换表达式）。级联面：**无**（不 DROP）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v_def text;
BEGIN

  -- 链守卫在本块也要有一份，且必须查**整个链**（三者任一缺失即 680 形态）——
  -- 我第一版只查了顶层视图，结果在 scratch 库的 680 形态下没拦住，直接撞上
  -- 下面的硬前置。这是本轮第三次「守卫写窄了」：块 2 的、块 3 的、块 1 的，
  -- 每次都要真跑一遍才暴露。
  --
  -- 本块随后 regclass 了顶层视图取 viewdef，所以它自己必须守着自己要碰的关系。
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '824: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup';
    RETURN;
  END IF;

  IF to_regclass('public.session_turn_details') IS NULL
     OR to_regclass('public.session_turn_details_hot') IS NULL THEN
    RAISE EXCEPTION 'migration 824 requires session_turn_details family (run 733 first)';
  END IF;

  v_def := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
  IF position('t.client_ip::inet' in v_def) = 0 THEN
    RAISE EXCEPTION '824: canonical view has no client_ip cast (run 816 first); '
      'this migration only replaces that expression and does not add the projection';
  END IF;
END $$;
DO $$
DECLARE
  base_has_fp   boolean;
  base_has_raw  boolean;
  middle_cols   text;
  append_cols   text := 'source.request_class, source.due_at, source.origin_stage, source.token_band, source.client_forwarded_for';
  lateral_h     text := 'h.request_class, h.due_at, h.origin_stage, h.token_band, h.client_forwarded_for';
  lateral_p     text := 'p.request_class, p.due_at, p.origin_stage, p.token_band, p.client_forwarded_for';
  inner_sel     text;
  proj          text;
  names         text;
  v2_ddl        text;
  v_def         text;
BEGIN
    -- 守卫搬到这里（2026-10-03，§9.65.3）。**放在真正要重建的块内**，RETURN
    -- 才拦得住它 —— 本块原先从块 1 的守卫「继承」了一个它拦不住的早退。
    --
    -- 守卫：**整个 view 链**缺一即 no-op（680 事故形态）。不能只查顶层视图 ——
    -- 本块 regclass 了两个 wrapper 视图并从其中一个取列清单，链不全时它会崩在
    -- 「relation does not exist」，那不是 no-op 是崩溃。
    IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
       OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
       OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
      RAISE NOTICE '824: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup';
      RETURN;
    END IF;

    -- 已是目标形态就不重建：CREATE OR REPLACE VIEW 要 AccessExclusiveLock，会与
    -- 每个读方冲突。实测并发读方持锁时这条 EXECUTE 直接超时，而本迁移刚打印过
    -- "nothing to do"。安装器**每次部署都重跑整条链**（applySQL 没有
    -- schema_migrations 跳过），所以不早退等于每次部署都无谓抢一次锁。
    v_def := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
    -- 已是**回退目标形态**（无 rate_limit_exceeded）就不重建。
    IF position('rate_limit_exceeded' in v_def) = 0 THEN
      RAISE NOTICE '824 down: request_status projection is already the pre-824 form; nothing to do';
      RETURN;
    END IF;
    SELECT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema='public'
                      AND table_name='request_logs_with_current_month_without_customer_id'
                      AND column_name='system_fingerprint'),
           EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema='public'
                      AND table_name='request_logs_with_current_month_without_customer_id'
                      AND column_name='raw_model_name')
      INTO base_has_fp, base_has_raw;
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

    SELECT COALESCE(string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum), '')
      INTO middle_cols
      FROM pg_attribute a
     WHERE a.attrelid = 'public.request_logs_with_current_month_without_request_class_due_at'::regclass
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname NOT IN ('credits_rate_multiplier', 'client_ip');
    IF middle_cols IS NULL OR middle_cols = '' THEN
      RAISE EXCEPTION 'migration 824: empty middle wrapper column list';
    END IF;
    inner_sel := middle_cols
      || ', ' || append_cols
      || ', v.credits_rate_multiplier, v.client_ip';

    -- 118 列契约顺序与 815 逐字相同；本迁移只把 client_ip 一行换成有源投影。
    proj := $proj$
NULL::bigint AS id
        , t.request_id AS request_id
        , t.ts AS ts
        , t.tenant_id::text AS tenant_id
        , (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id
        , (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) AS api_key_id
        , t.end_user_id AS end_user_id
        , d.client_model AS client_model
        , t.model AS outbound_model
        , (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id
        , d.provider_id AS provider_id
        , t.canonical_id AS canonical_id
        , d.client_profile AS client_profile
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
        , d.virtual_ip AS virtual_ip
        , d.virtual_mac AS virtual_mac
        , d.affinity_hit AS affinity_hit
        , t.stream_first_chunk_ms AS stream_first_chunk_ms
        , t.stream_chunk_count AS stream_chunk_count
        , t.stream_interrupted AS stream_interrupted
        , t.stream_done_sent AS stream_done_sent
        , t.request_checksum AS request_checksum
        , t.response_checksum AS response_checksum
        , d.transform_rule_id AS transform_rule_id
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
        , d.gw_task_id AS gw_task_id
        , (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
        , d.api_key_prefix AS api_key_prefix
        , d.owner_user AS owner_user
        , d.application_code AS application_code
        , d.key_alias AS key_alias
        , d.api_key_owner_user AS api_key_owner_user
        , t.is_auto_request AS is_auto_request
        , t.task_type AS task_type
        , d.auto_profile AS auto_profile
        , (CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END) AS auto_decision
        , t.auto_confidence::numeric(4,3) AS auto_confidence
        , t.work_type AS work_type
        , t.task_type_chosen AS task_type_chosen
        , d.confidence_num AS confidence_num
        , d.model_chosen AS model_chosen
        , d.strategy_used AS strategy_used
        , t.credits_charged AS credits_charged
        , t.parent_request_id AS parent_request_id
        , d.compression_reason AS compression_reason
        , t.compression_strategy AS compression_strategy
        , t.compression_meta AS compression_meta
        , d.outbound_msg_count AS outbound_msg_count
        , d.outbound_token_est AS outbound_token_est
        , d.outbound_msg_hashes AS outbound_msg_hashes
        , d.quality_flags AS quality_flags
        , d.quality_fix_actions AS quality_fix_actions
        , d.quality_score AS quality_score
        , t.upstream_finish_reason AS upstream_finish_reason
        , t.tools AS tool_calls
        , t.client_endpoint AS client_endpoint
        , t.client_timeout AS client_timeout
        , d.stream_chunk_errors AS stream_chunk_errors
        , d.stream_chunks_sent AS stream_chunks_sent
        , t.client_request_id AS client_request_id
        , t.upstream_status_code AS upstream_status_code
        , NULL::text[] AS test_col
        , NULL::text AS test_tab_indent
        , NULL::text AS provider_model
        , d.attachments AS attachments
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
        , d.request_type AS request_type
        , t.is_final_success AS is_final_success
        , t.origin_actor::character varying(255) AS origin_actor
        , t.customer_id AS customer_id
        , d.request_class AS request_class
        , d.due_at AS due_at
        , t.system_fingerprint AS system_fingerprint
        , t.raw_model_name AS raw_model_name
        , NULL::double precision AS credits_rate_multiplier
        , (CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END) AS client_ip
        , t.origin_stage AS origin_stage
        , t.token_band AS token_band
        , t.client_forwarded_for AS client_forwarded_for
$proj$;

    names := $names$id, request_id, ts, tenant_id, application_id, api_key_id, end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id, client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens, identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id, egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview, stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id, request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user, is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type, task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint, client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col, test_tab_indent, provider_model, attachments, has_attachments, attachment_count, routing_attempts, routing_summary, agent_name, agent_type, client_protocol, canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type, is_final_success, origin_actor, customer_id, request_class, due_at, system_fingerprint, raw_model_name, credits_rate_multiplier, client_ip, origin_stage, token_band, client_forwarded_for$names$;

    v2_ddl := format($ddl$
    CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
    SELECT %s
    FROM public.session_turns_hot t
    LEFT JOIN public.session_turn_details_hot d
      ON d.tenant_id = t.tenant_id
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    UNION ALL
    SELECT %s
    FROM public.session_turns t
    LEFT JOIN public.session_turn_details d
      ON d.tenant_id = t.tenant_id
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    UNION ALL
    SELECT %s
    FROM (
      SELECT %s
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
  $ddl$, proj, proj, names, inner_sel, lateral_h, lateral_p);

    EXECUTE v2_ddl;

    COMMENT ON VIEW public.request_logs_with_current_month IS
      '会话存储解耦 v3（824 已回滚）: 710 拼装体 + 734 details 特征层 LEFT JOIN（733）+ '
      '815 三列尾追加 + 816 client_ip 有源投影 + 817 语义守卫 pg_input_is_valid；'
      'request_status 会话腿已退回 824 之前只看 status_code=429 的形态。'
      '仍 NULL：id（v1 请求行 id ≠ session turn id）/'
      'test_col/test_tab_indent/provider_model/credits_rate_multiplier，'
      '以及 client_ip 本身（字面量非法时落 NULL，不抛错）。'
      'trace_events 刻意未投影（镜像未写该列，投影即净数据损失）。';
END $$;




-- 列数与形态对账（fail-closed）。列数仍是 118。
DO $$
DECLARE
  cnt   integer;
  v_def text;
BEGIN
  -- 链守卫与块 1/块 2 **同形**（查整个链）。只查顶层视图是不够的：680 形态下
  -- 顶层视图可能是完好的一个陈旧版本，于是本块会拿它去做 118 列对账并硬失败
  -- ——而此时块 2 明明已经决定「不重建」，对账的前提根本不存在。
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '824: view chain incomplete; skipping post-rebuild verification';
    RETURN;
  END IF;
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 118 THEN
    RAISE EXCEPTION '824: column count = % (expected 118); aborting migration', cnt;
  END IF;
  v_def := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
  -- 回退判据：rate_limit_exceeded 那一臂必须**消失**。
  IF position('rate_limit_exceeded' in v_def) > 0 THEN
    RAISE EXCEPTION '824 down: the error_kind arm is still present; the rollback did not converge';
  END IF;
  -- 817 的语义守卫必须**保持**——拿 817 的 up 当基准就是为了不把它弄丢，
  -- 这条断言把「基准选错了」这个错误变成可观测的失败。
  IF position('pg_input_is_valid' in v_def) = 0 THEN
    RAISE EXCEPTION '824 down: 817 semantic client_ip guard was lost — the rollback base was wrong';
  END IF;
  RAISE NOTICE '824 down: column count = 118 OK, request_status reverted to the pre-824 form';
END $$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('824', 'ROLLED BACK: request_status session-leg projection reverted to the pre-824 form (status_code=429 arm only). 437,402 rate-limited turns are reported as failure again (audit §9.160)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
