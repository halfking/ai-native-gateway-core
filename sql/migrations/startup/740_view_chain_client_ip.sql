-- ===========================================================================
-- File:          sql/migrations/startup/740_view_chain_client_ip.sql
-- Migration:     740
-- Database:      llm_gateway
-- Purpose:       R57 B7（docs/audit/2026-09-23-r56-48h-audit-round.md §三.1）
--                virtual_ip 假名 IP 死分支的数据源级修复：view 链
--                request_logs_with_current_month* 补投影 client_ip（真实
--                客户端 IP，migration 341 起落在 request_logs[_hot]，
--                origin 中间件信任表解析），让 bg/stats_minute_rollup 的
--                client_ip 维度与看板 fallback 饼图从假名 10.x（identity
--                hash 派生，恒命中 classifyVirtualIP 的内网直显臂）切到
--                真源——设计 §7「内网直显 IP、外网归类国家·省·市」的
--                GeoIP 段表臂自此才对真实数据可达。
--
--                本迁移遵循 738 同款「viewdef 捕获 → 逐 view 独立守卫 →
--                DROP CASCADE → regexp 补列 → 重建 → 列数对账 EXCEPTION」
--                惯用法。三 view 独立幂等：viewdef 已含 client_ip 时跳过
--                该 view（708 历史教训）。
--
--                顶层四臂锚点（738 的 credits_rate_multiplier 列即插在
--                各臂末，pg_get_viewdef 输出实测）：session_turns_hot /
--                session_turns 两臂 NULL::inet 补位（session 侧 client_ip
--                为 text 且未回填，738 NULL 补位同款）；v1 臂内层补
--                v.client_ip、外层补 rl.client_ip。
--
--                下游消费方（同轮落地）：bg/stats_minute_rollup 新增
--                client_ip 维度（virtual_ip 旧维度保留为遗留对照，同
--                client_profile/agent_name 先例）；admin 看板 client_ips
--                饼图（原 virtual_ips）走真源 + GeoIP 归类。
--
-- Dependencies:  738（credits_rate_multiplier 已在链上——v1 臂锚点是其
--                插入行）、733/734（session 特征层拼装体）、736（倍率列）。
-- Idempotent:    是（逐 view 独立判断，跳过即 no-op）。
-- Down:          740_view_chain_client_ip.down.sql（viewdef 捕获 → regexp
--                剥离 client_ip 行 → 重建，同样逐 view 独立幂等）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v_bot text;
  v_mid text;
  v_bot_ddl text;
  v_mid_ddl text;
  v_bot_has  boolean;
  v_mid_has  boolean;
  v_top_has  boolean;
BEGIN
  -- ── 0. 守卫：view 链缺一即 no-op（680 事故形态；db.ensure 会以含
  --       client_ip 的新契约自愈重建）────────────────────────────────────
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '740: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup';
    RETURN;
  END IF;

  -- 底表无 client_ip 列（<341 的极简库）同样跳过：补列无源。
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public'
      AND table_name='request_logs_hot'
      AND column_name='client_ip'
  ) THEN
    RAISE NOTICE '740: request_logs_hot.client_ip missing (pre-341); skipping';
    RETURN;
  END IF;

  -- 顶层重建体（734 骨架）引用 session_turn_details 特征层——733 在序列
  -- 中先于本迁移，缺族即链外状态，fail-closed（734 同款 EXCEPTION）。
  IF to_regclass('public.session_turn_details') IS NULL
     OR to_regclass('public.session_turn_details_hot') IS NULL THEN
    RAISE EXCEPTION 'migration 740 requires session_turn_details family (run 733 first)';
  END IF;

  -- 每个 view 独立判断（information_schema 精确探测，与 738 同款）。
  v_bot_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_customer_id'
                           AND column_name='client_ip');
  v_mid_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_request_class_due_at'
                           AND column_name='client_ip');
  v_top_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month'
                           AND column_name='client_ip');

  IF v_bot_has AND v_mid_has AND v_top_has THEN
    RAISE NOTICE '740: view chain already exposes client_ip on all 3 views; nothing to do';
    RETURN;
  END IF;

  -- ── 1. 仅捕获需要重建的 viewdef ────────────────────────────────────
  IF NOT v_bot_has THEN
    v_bot := pg_get_viewdef('public.request_logs_with_current_month_without_customer_id'::regclass, true);
  END IF;
  IF NOT v_mid_has THEN
    v_mid := pg_get_viewdef('public.request_logs_with_current_month_without_request_class_due_at'::regclass, true);
  END IF;

  -- ── 2. 仅 drop 需要重建的 view（自顶向下；顶层走 CREATE OR REPLACE
  --       追加列语义，不 drop——避免再次级联击杀探测健康视图族，716 家族
  --       由 db.ensureProbeHealthDashboardViews 在启动期自愈）──────────────
  IF NOT v_mid_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
  END IF;
  IF NOT v_bot_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;
  END IF;

  -- ── 3. 底层 view（hot∪parent）双臂末尾（credits_rate_multiplier 行后）
  --       补 client_ip。[[:space:]]* 容忍 pg_get_viewdef 行续/缩进。
  IF NOT v_bot_has THEN
    v_bot_ddl := regexp_replace(
      v_bot,
      E'\n[[:space:]]+FROM request_logs_hot\n',
      E'\n   , request_logs_hot.client_ip\n   FROM request_logs_hot\n'
    );
    v_bot_ddl := regexp_replace(
      v_bot_ddl,
      E'\n[[:space:]]+FROM request_logs;',
      E'\n   , request_logs.client_ip\n   FROM request_logs;'
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS ' || v_bot_ddl;
  END IF;

  -- ── 4. 中层 view（v.* + m.customer_id + 738 的 v.credits_rate_multiplier
  --       末列后追加 v.client_ip）──
  IF NOT v_mid_has THEN
    v_mid_ddl := regexp_replace(
      v_mid,
      E'\n[[:space:]]+v\.credits_rate_multiplier\n[[:space:]]+FROM request_logs_with_current_month_without_customer_id v',
      E'\n    v.credits_rate_multiplier\n   , v.client_ip\n   FROM request_logs_with_current_month_without_customer_id v'
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS ' || v_mid_ddl;
  END IF;

  -- ── 5. 顶层 view 全量重建（734 骨架 + 738/740 追加尾列）────────────────
  --    刻意不用 v.* 星：其展开内容随中层扩列时变（710/734 的存储展开冻结于
  --    各自 CREATE 时刻），既无法与 db.ensure 组合体（canonicalV2DDL）逐字
  --    对齐，也无法被 740.down 稳定还原成 738 形态。内层显式列出中层列
  --    （剔除 credits/client_ip，与 ensure 的 middleWrapperCols 同一条查询），
  --    738/740 两列以 v.<col> 文本引用固定在内层末尾（= 738 regexp 插入位）。
  IF NOT v_top_has THEN
    DECLARE
      base_has_fp boolean;
      base_has_raw boolean;
      middle_cols  text;
      append_cols text := 'source.request_class, source.due_at';
      lateral_h   text := 'h.request_class, h.due_at';
      lateral_p   text := 'p.request_class, p.due_at';
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

      --    5a) 中层显式列清单（剔除 738/740 两列；与 db.middleWrapperCols
      --        同一条查询，保证 ensure↔迁移 viewdef 逐字等价）。
      SELECT COALESCE(string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum), '')
        INTO middle_cols
        FROM pg_attribute a
       WHERE a.attrelid = 'public.request_logs_with_current_month_without_request_class_due_at'::regclass
         AND a.attnum > 0 AND NOT a.attisdropped
         AND a.attname NOT IN ('credits_rate_multiplier', 'client_ip');
      IF middle_cols IS NULL OR middle_cols = '' THEN
        RAISE EXCEPTION 'migration 740: empty middle wrapper column list';
      END IF;
      inner_sel := middle_cols
        || ', ' || append_cols
        || ', v.credits_rate_multiplier, v.client_ip';

      --    5b) 会话分支 115 列投影（= 734 proj 逐字 + 738/740 两个补位尾列）。
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
$proj$;

      --    5c) 115 列契约顺序（= 734 names + credits_rate_multiplier + client_ip）。
      names := $names$id, request_id, ts, tenant_id, application_id, api_key_id, end_user_id, client_model, outbound_model, credential_id, provider_id, canonical_id, client_profile, request_mode, prompt_tokens, completion_tokens, total_tokens, cost_usd, latency_ms, success, error_kind, search_text, cache_read_tokens, cache_write_tokens, identity_hash, virtual_client_id, virtual_ip, virtual_mac, affinity_hit, stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent, request_checksum, response_checksum, transform_rule_id, egress_protocol, failure_stage, failure_detail_code, request_preview, transform_summary, response_preview, stream_done_received, cost_display, cost_currency, usage_source, gw_session_id, gw_task_id, request_status, api_key_prefix, owner_user, application_code, key_alias, api_key_owner_user, is_auto_request, task_type, auto_profile, auto_decision, auto_confidence, work_type, task_type_chosen, confidence_num, model_chosen, strategy_used, credits_charged, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score, upstream_finish_reason, tool_calls, client_endpoint, client_timeout, stream_chunk_errors, stream_chunks_sent, client_request_id, upstream_status_code, test_col, test_tab_indent, provider_model, attachments, has_attachments, attachment_count, routing_attempts, routing_summary, agent_name, agent_type, client_protocol, canonical_model, t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at, t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at, t8_response_start_at, t9_response_end_at, request_type, is_final_success, origin_actor, customer_id, request_class, due_at, system_fingerprint, raw_model_name, credits_rate_multiplier, client_ip$names$;

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
    END;
  END IF;
END $$;

-- 列数对账（fail-closed，738/R56 先例：ON_ERROR_STOP 不拦 WARNING，
-- regexp 未命中静默交付残缺视图链正是要防的事故形态）。
DO $$
DECLARE
  cnt integer;
BEGIN
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month_without_customer_id';
  IF cnt <> 110 THEN
    RAISE EXCEPTION '740: request_logs_with_current_month_without_customer_id column count = % (expected 110); bottom view rebuild did not converge — aborting migration (738 idiom, frozen view bodies 680/717/734)', cnt;
  END IF;
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month_without_request_class_due_at';
  IF cnt <> 111 THEN
    RAISE EXCEPTION '740: request_logs_with_current_month_without_request_class_due_at column count = % (expected 111); middle view rebuild did not converge — aborting migration', cnt;
  END IF;
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 115 THEN
    RAISE EXCEPTION '740: request_logs_with_current_month column count = % (expected 115); view chain rebuild did not converge — aborting migration', cnt;
  END IF;
  RAISE NOTICE '740: view chain column counts = 110/111/115 OK';
END $$;

-- Ledger self-registration（710/734/738 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('740', 'view chain exposes client_ip (R57 B7: virtual_ip dim is identity-derived pseudo 10.x — board geo classification unreachable; rollup/board switch to the real client_ip source, 341 column now projected through request_logs_with_current_month*)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
