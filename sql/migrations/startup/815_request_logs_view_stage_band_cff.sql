-- ===========================================================================
-- File:          sql/migrations/startup/815_request_logs_view_stage_band_cff.sql
-- Migration:     815
-- Database:      llm_gateway
-- Purpose:       存储优化方案 v2 S2 收尾：canonical 视图补 3 个尾列
--                origin_stage / token_band / client_forwarded_for
--                （审计 §9.22 决策 1）。顶层 view 全量重建为 118 列契约：
--                740 的 115 列体 + 3 列，列序接在 client_ip 之后。
--
--                动机（不是「补齐」而是修一个线上缺陷）：
--                  - admin/compression_stats.go:212 的 token 分带聚合读
--                    `token_band FROM request_logs_with_current_month`，而
--                    token_band 从未在视图契约内 ⇒ 每调必 42703，且错误被
--                    slog.Warn 吞掉 ⇒ 仪表盘该格长期静默为空。修复后本列进
--                    契约，具名豁免（admin/view_source_column_contract_test.go
--                    的 compression_stats.go:212）可随之删除。
--                  - origin_stage 让视图读方不必为探测排除谓词绕开视图
--                    （bg.ProbeTrafficExclusionPredicateView 已用 origin_actor
--                    代理，语义更弱）。
--
--                三列的数据前提（真库实测，2026-10-02，非推断）：
--                  - session_turns / session_turns_hot 双侧均已有该三列；
--                  - 与 v1 按 request_id 配对 1,515,960 行逐值比对：
--                    **两侧都有值时不一致行数 = 0**（token_band 53,566 /
--                    cff 177,199 / origin_stage 177,199 行的差异全部是
--                    「v1 有值、session 侧为 NULL」的覆盖缺口，不是语义分歧）；
--                  - 覆盖缺口只发生在历史：近 1 天 origin_stage/cff 非空率
--                    76%，近 7 天 86%（镜像在持续补齐）。
--
--                **trace_events 同批刻意不投影**：它在 session_turns 上
--                1d/7d/30d 三个窗口的非空率恒为 0（镜像从不写它），而 v1 侧
--                有 691,883 行带值、且这些行已被反连接丢弃 ⇒ 投影它等于把
--                691,883 行真实值换成 NULL，是 §9.18「修好了但变全盲」的同一
--                形状。正解是先让镜像写该列，再投影（登记在 §9.22 遗留）。
--
--                **id 刻意不投影**（决策 1 的待确认项，本迁移给出实测结论）：
--                真库 1,515,984 组同 request_id 配对里 `r.id = t.id` 命中
--                **0** 次 —— v1 的 request_logs.id 是请求行 id、session_turns.id
--                是 turn id，判据是「同一个东西」而不是「session 侧有这个名字」。
--
-- Dependencies:  740（115 列体与 v.credits/v.client_ip 内层尾引用）、
--                733/734（session 特征层拼装体，缺族即 EXCEPTION）。
-- Idempotent:    是（顶层 view 已有三列即 no-op；CREATE OR REPLACE 走追加
--                列语义，不 DROP——不二次级联击杀探测健康视图族）。
-- Go 镜像体:     db/request_logs_view_schema.go（canonicalColumnOrderV2 +
--                projectionExprsV2 + canonicalV2DDL，启动自愈同体）；等价性
--                由 db/view_schema_v2_contract_test.go 校验。
-- Down:          815_request_logs_view_stage_band_cff.down.sql（viewdef 捕获
--                → regexp 剥离三行 → 重建 740 形态）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v_top_has boolean;
BEGIN
  -- ── 0. 守卫：view 链缺一即 no-op（680 事故形态；db.ensure 启动自愈会带上
  --       三列重建）──────────────────────────────────────────────────────
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '815: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup';
    RETURN;
  END IF;

  -- 顶层重建体引用 session_turn_details 特征层 —— 733 缺族即链外状态，
  -- fail-closed（734/740 同款 EXCEPTION，不静默交付 710 形态）。
  IF to_regclass('public.session_turn_details') IS NULL
     OR to_regclass('public.session_turn_details_hot') IS NULL THEN
    RAISE EXCEPTION 'migration 815 requires session_turn_details family (run 733 first)';
  END IF;

  -- 三列的源必须齐备：会话侧走 t.<col> 直映，v1 侧走 lateral。三者缺一就
  -- 补不出有源的列 —— 补 NULL 补位等于把「缺源」伪装成「已迁移」。
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_turns'
      AND column_name IN ('origin_stage','token_band','client_forwarded_for')
    HAVING count(*) = 3
  ) THEN
    RAISE NOTICE '815: session_turns lacks one of origin_stage/token_band/client_forwarded_for; skipping';
    RETURN;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='request_logs_hot'
      AND column_name IN ('origin_stage','token_band','client_forwarded_for')
    HAVING count(*) = 3
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='request_logs'
      AND column_name IN ('origin_stage','token_band','client_forwarded_for')
    HAVING count(*) = 3
  ) THEN
    RAISE NOTICE '815: request_logs(_hot) lacks one of the three source columns; skipping';
    RETURN;
  END IF;

  v_top_has := (SELECT count(*) = 3 FROM information_schema.columns
                 WHERE table_schema='public'
                   AND table_name='request_logs_with_current_month'
                   AND column_name IN ('origin_stage','token_band','client_forwarded_for'));
  IF v_top_has THEN
    RAISE NOTICE '815: canonical view already exposes the three columns; nothing to do';
    RETURN;
  END IF;

  DECLARE
    base_has_fp boolean;
    base_has_raw boolean;
    middle_cols  text;
    append_cols text := 'source.request_class, source.due_at, source.origin_stage, source.token_band, source.client_forwarded_for';
    lateral_h   text := 'h.request_class, h.due_at, h.origin_stage, h.token_band, h.client_forwarded_for';
    lateral_p   text := 'p.request_class, p.due_at, p.origin_stage, p.token_band, p.client_forwarded_for';
    proj        text;
    names       text;
    inner_sel   text;
    v2_ddl      text;
  BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'request_logs_with_current_month_without_customer_id'
          AND column_name = 'system_fingerprint'
    ), EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'request_logs_with_current_month_without_customer_id'
          AND column_name = 'raw_model_name'
    ) INTO base_has_fp, base_has_raw;
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

    -- 中层显式列清单（剔除 738/740 两列；与 db.middleWrapperCols 同一条
    -- 查询，保证 ensure ↔ 迁移 viewdef 逐字等价）。
    SELECT COALESCE(string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum), '')
      INTO middle_cols
      FROM pg_attribute a
     WHERE a.attrelid = 'public.request_logs_with_current_month_without_request_class_due_at'::regclass
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname NOT IN ('credits_rate_multiplier', 'client_ip');
    IF middle_cols IS NULL OR middle_cols = '' THEN
      RAISE EXCEPTION 'migration 815: empty middle wrapper column list';
    END IF;
    inner_sel := middle_cols
      || ', ' || append_cols
      || ', v.credits_rate_multiplier, v.client_ip';

    -- 会话分支 118 列投影 = 740 的 proj 逐字 + 三列直映。
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
        , NULL::inet AS client_ip
        , t.origin_stage AS origin_stage
        , t.token_band AS token_band
        , t.client_forwarded_for AS client_forwarded_for
$proj$;

    -- 118 列契约顺序 = 740 的 115 + 三列（UNION ALL 按位置匹型）。
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
      '会话存储解耦 v3（815）: 710 拼装体 + 734 details 特征层 LEFT JOIN（733）+ '
      '815 三列尾追加（origin_stage/token_band/client_forwarded_for；session 侧 '
      '直映 t.<col>、v1 侧 lateral）。仍 NULL：id（v1 请求行 id ≠ session turn id）/'
      'test_col/test_tab_indent/provider_model。trace_events 刻意未投影（镜像从不'
      '写，投影即净数据损失）。';
  END;
END $$;

-- 列数对账（fail-closed，738/740 惯例：ON_ERROR_STOP 不拦 WARNING，
-- 静默交付残缺视图链正是要防的事故形态）。
DO $$
DECLARE
  cnt integer;
BEGIN
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 118 THEN
    RAISE EXCEPTION '815: request_logs_with_current_month column count = % (expected 118); view rebuild did not converge — aborting migration', cnt;
  END IF;
  -- 三列必须真的有源：NULL 补位冒充「已迁移」是本轮明确拒绝的形态。
  IF (SELECT count(*) FROM information_schema.columns
       WHERE table_schema='public' AND table_name='request_logs_with_current_month'
         AND column_name IN ('origin_stage','token_band','client_forwarded_for')) <> 3 THEN
    RAISE EXCEPTION '815: canonical view is missing one of the three columns after rebuild';
  END IF;
  RAISE NOTICE '815: request_logs_with_current_month column count = 118 OK';
END $$;

-- Ledger self-registration（710/734/738/740 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('815', 'storage plan v2 S2: canonical view appends origin_stage/token_band/client_forwarded_for (115 -> 118); fixes compression_stats token_band 42703; trace_events/id deliberately excluded (audit §9.27)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
