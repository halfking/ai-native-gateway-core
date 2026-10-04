-- ===========================================================================
-- File:          sql/migrations/startup/824_request_status_rate_limited_projection.sql
-- Migration:     824
-- Database:      llm_gateway
-- Purpose:       修掉 canonical 视图会话腿的 `rate_limited` 分类**从未生效**。
--
-- 缺陷（审计 §9.160，本地真库 1,688,629 行 session_turns 实测）：
--   会话腿的 `request_status` 表达式是
--     CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success'
--          WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END
--   而 `session_turns` 里 **`status_code = 429` 一行都没有**（全表 0 行，
--   且 status_code 本身无 NULL）。真限流轮次在会话侧记的是 **500**。
--   ⇒ 那个 `rate_limited` 分支在会话腿上是**死代码，永远不会触发**。
--
-- 后果（不是退役才发生，**今天就是错的**）：
--   - 437,402 条真限流被视图报成普通 `failure`（占 session_turns 25.9%）；
--   - 旧推导 failure 1,358,245 / success 330,384 / rate_limited **0**；
--     新推导 failure 920,843 / success 330,384 / rate_limited 437,402
--     ⇒ 视图把 failure **高估了 32%**；
--   - 视图当前能看到的 rate_limited 只有 8,417 条，全部来自 v1 冻结腿；
--   - request_logs 一旦退役，全系统 rate_limited 归零。
--
-- 为什么 error_kind 是权威判据（穷尽交叉表，不是抽样）：
--   `error_kind = 'rate_limit_exceeded'` ⟺ v1 `request_status = 'rate_limited'`
--   在 394,614 组孪生行上**双向零反例**（A: v1 是 rate_limited 而 ek 不是 = 0；
--   B: ek 是 rate_limit_exceeded 而 v1 不是 = 0），且这 437,402 行
--   `success` 全部为 false（分支序安全）。
--   交叉核对：394,614（有孪生）+ 42,788（无孪生）= 437,402，对得上。
--   ⇒ 限流信号在镜像里**没有丢**，只是视图不去看它。
--
-- 为什么不动 session_turns 的数据：
--   `status_code=500` 是不是写方该改，属另一件事（可能上游确实回了 500，
--   网关本地限流另记），本迁移只修**读侧分类**，不碰任何行。
--
-- 为什么不顺带读 823 的 `request_status` 列：
--   那会引入对迁移 823 的**硬依赖**——823 未跑的库上重建视图会直接
--   undefined column 失败。error_kind 在冻结 113 列契约里，恒在。
--   823 那条路留给回填完成后的 D9 裁决（见决策表）。
--
-- 列序/列数:     **118 → 118，不变**（换表达式，不是增删列）。
-- Idempotent:    是（viewdef 里已含 `rate_limit_exceeded` 即 no-op）。
--                判据**只能单向**：新式是旧式的严格超集（保留了
--                status_code=429 那臂），所以「旧式是否已消失」没有可测形式。
--                这里不编一条永远为真的假检查。
-- Dependencies:  817（链上最后一条重建迁移；本迁移沿用它的全量 proj 块，
--                只换 request_status 一行）。
-- Go 镜像体:     db/request_logs_view_schema.go（sessionRequestStatusExpr），
--                等价性由 db/view_schema_v2_contract_test.go 校验。
-- Down:          824_..._rate_limited_projection.down.sql（**确定性重建回 817
--                形态**，即旧式表达式 + 语义 client_ip 守卫）。
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
    -- 幂等判据只能是**单向**的：824 引入的 `rate_limit_exceeded` 字面量在
    -- 824 之前的任何形态里都不存在（本地真库实测现网 viewdef 出现 0 次），
    -- 而 pg_get_viewdef 对字面量是逐字保留的（只重排 CASE 的 WHEN/END 排版），
    -- 所以它是稳定判据。反方向不成立：新式是旧式的**严格超集**（保留了
    -- `WHEN t.status_code = 429 THEN 'rate_limited'` 那臂），旧式是它的子串，
    -- 「旧式是否消失」没有任何可测形式。这里不写一条恒真的假检查。
    v_def := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
    IF position('rate_limit_exceeded' in v_def) > 0 THEN
      RAISE NOTICE '824: canonical view already projects rate_limited from error_kind; nothing to do';
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
        , (CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.error_kind = 'rate_limit_exceeded' THEN 'rate_limited' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status
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
      '会话存储解耦 v3（824）: 710 拼装体 + 734 details 特征层 LEFT JOIN（733）+ '
      '815 三列尾追加 + 816 client_ip 有源投影 + 817 语义守卫 pg_input_is_valid + '
      '824 request_status 会话腿改走 error_kind=rate_limit_exceeded。'
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
  req_status_type text;
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
  -- 824 的落地判据：`rate_limit_exceeded` 必须在 viewdef 里。
  IF position('rate_limit_exceeded' in v_def) = 0 THEN
    RAISE EXCEPTION '824: request_status projection did not pick up the error_kind arm';
  END IF;
  -- 817 的语义守卫不能被本迁移弄丢（824 沿用 817 的全量 proj 块，理论上
  -- 不可能丢，但这是「重建整条视图」该有的回归防线——代价只是一次字符串查）。
  IF position('pg_input_is_valid' in v_def) = 0 THEN
    RAISE EXCEPTION '824: 817 semantic client_ip guard missing after rebuild';
  END IF;
  -- request_status 必须仍是 text：投影是 CASE 链，PG 按首臂推类型，若首臂被
  -- 改成常量会把整列类型带偏，而整条 UNION ALL 的位置类型必须与 v1 腿一致。
  SELECT data_type INTO req_status_type
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month'
     AND column_name='request_status';
  IF req_status_type IS DISTINCT FROM 'text' THEN
    RAISE EXCEPTION '824: request_status column type = %, expected text', req_status_type;
  END IF;
  -- **不做**「旧式必须消失」的反向检查：新式是旧式的严格超集（保留了
  -- status_code=429 那臂），反向判据不存在可测形式，写了就是恒真的假门。
  -- 分类正确性由真库门禁验证（db/request_status_projection_realdb_test.go），
  -- 不由本迁移假装验证。
  RAISE NOTICE '824: column count = 118 OK, request_status now classifies rate_limit_exceeded from error_kind';
END $$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('824', 'storage plan v2: classify rate_limited from session_turns.error_kind = ''rate_limit_exceeded''; the status_code=429 arm was dead code (0 of 1,688,629 rows) so 437,402 real rate-limited turns were reported as plain failure, overstating failure by 32% (audit §9.160)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
