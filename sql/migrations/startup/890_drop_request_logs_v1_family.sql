-- ===========================================================================
-- File:          sql/migrations/startup/890_drop_request_logs_v1_family.sql
-- Migration:     890
-- Database:      llm_gateway
-- Purpose:       v1 `request_logs` 族的**最终删除**（退役链的最后一跳，L12）。
--
--                ① canonical 视图 `request_logs_with_current_month` 去掉第三臂
--                   （冻结 v1 体 + 双反连接去重），只剩两臂
--                   session_turns_hot ∪ session_turns（734 details 特征层）；
--                ② 删掉 4 张 v1-only 视图（577/610/734 建的中层包装链 + bodies 族）；
--                ③ 删掉 v1 族的全部表（母表带分区一起删）、hot 表、序列；
--                ④ 删掉只服务 v1 族的分区管理函数与两个触发器函数。
--
-- 依赖：430/513/526（session_turns 族）、733/734（details 特征层）、860（改名）。
-- Down: 890_drop_request_logs_v1_family.down.sql
--
-- ⚠⚠⚠ 破坏性迁移：v1 族里的行是「会话族镜像建立之前」的历史，删表即删历史。
--    ⇒ 本文件带**非空闸**：族里有行就 RAISE EXCEPTION，把决定权交回人。
--    ⇒ 本机实测 4 张 v1 表全 0 行（停写后从未再写），闸放行。
--    ⇒ 生产（211/252）若还有 2026-10-16 停写前的历史，本迁移会**拒绝执行**
--      ——这是刻意的：先归档/核账，再删。
--
-- ⚠ 命名代际：本文件按**两种物理名**都写（`request_logs` 与 `_request_logs`），
--    因为三条投递通道对"改名后"的处理**不一致**：
--      · 部署通道 scripts/apply-db-revision-sequence.sh 会把文件里的
--        `request_logs` 词法改写成 `_request_logs`（代际探测：renamed/legacy）；
--      · 安装器 installer/internal/dbinit/runner.go **不做**这个改写，
--        逐字执行；
--      · 新装路径在 860 之后族已是 `_request_logs`。
--    ⇒ 用"两种名字都试、存在才删"的写法，三条通道都正确。
--    （`LIKE` 里配 `_` 是**单字符通配符**（审计 §10.8），本文件不靠它判名字。）
-- ===========================================================================

BEGIN;

-- 锁守卫（>870 的启动迁移必带，installer/cmd/llm-gw-installer/
-- startup_ddl_lock_guard_gate_test.go）：DROP TABLE 要 AccessExclusiveLock，
-- 而 v1 族在生产上是**分区大表** ⇒ 等锁可能堵住全库。
SET LOCAL lock_timeout = '10s';
SET LOCAL statement_timeout = '300s';

-- ── 0. 前置：会话族必须就位 ────────────────────────────────────────────────
-- ★ 顺序是**硬约束**：本迁移把 canonical 视图换成会话两臂体，若会话族不在，
--   视图就退化成"引用不存在的关系" ⇒ 每个读方 42P01。
DO $$
BEGIN
  IF to_regclass('public.session_turns') IS NULL
     OR to_regclass('public.session_turns_hot') IS NULL THEN
    RAISE EXCEPTION
      'migration 890 requires the session_turns family (run 430/513/526 first)';
  END IF;
  IF to_regclass('public.session_turn_details') IS NULL
     OR to_regclass('public.session_turn_details_hot') IS NULL THEN
    RAISE EXCEPTION
      'migration 890 requires the session_turn_details family (run 733/734 first)';
  END IF;
END $$;

-- ── 1. ★ 非空闸：v1 族必须为空，否则拒绝删表 ───────────────────────────────
-- 为什么要这道闸：v1 族的行 = 会话族镜像建立**之前**的历史。删表 = 删历史，
-- 而且是**不可回滚**的那种（down 只能重建骨架，救不回行）。
-- ⇒ 把"要不要丢历史"变成一个必须显式回答的问题，而不是迁移的副作用。
--
-- ⚠ 用**动态 SQL**逐个统计，而不是写死表名：写死表名会被部署通道的词法
--   改写改掉（见文件头的命名代际说明），而"两种名字都试"在动态 SQL 里
--   天然成立。
DO $$
DECLARE
  v_name text;
  v_cnt  bigint;
BEGIN
  FOREACH v_name IN ARRAY ARRAY[
      'request_logs', '_request_logs',
      'request_logs_hot', '_request_logs_hot',
      'request_logs_bodies', '_request_logs_bodies',
      'request_logs_bodies_hot', '_request_logs_bodies_hot',
      'request_logs_archive', '_request_logs_archive'
  ] LOOP
    IF to_regclass('public.' || v_name) IS NOT NULL THEN
      EXECUTE format('SELECT count(*) FROM public.%I', v_name) INTO v_cnt;
      IF v_cnt > 0 THEN
        RAISE EXCEPTION
          'migration 890 refuses to drop a NON-EMPTY v1 family: public.% has % rows. '
          'The v1 rows predate the session-family mirror and are NOT recoverable by the '
          'down script (skeleton only). Archive/verify them first, then re-run 890.',
          v_name, v_cnt;
      END IF;
    END IF;
  END LOOP;
END $$;

-- ── 2. canonical 视图换成**会话两臂体** ────────────────────────────────────
-- ⚠ 顺序硬约束：必须**先**换视图、**后**删中层包装视图。
--   旧 canonical 的第三臂读 `request_logs_with_current_month_without_request_class_due_at`，
--   而它又读 `..._without_customer_id` ⇒ 视图不先换，删包装视图会被依赖挡住。
--
-- ⚠ 两臂的投影文本与 710/734/813/815/816/817/824 装出来的**逐字相同**
--   ⇒ 118 列同名同序 ⇒ 约 40 个读方零改动。
--   （与 db/request_logs_view_schema.go 的 canonicalSessionOnlyDDL 同源；
--     等价性由 db/view_schema_v2_contract_test.go 守着。）
--
-- ⚠ 若 details 族不在（第 0 步已保证它在），LEFT JOIN 特征层必须缺席 ⇒
--   投影退化为 710 的 NULL 占位形态。本迁移要求 details 在场，
--   因此固定走 details 形态。
DO $$
DECLARE
  proj TEXT;
BEGIN
  proj := $proj$
NULL::bigint AS id,
		t.request_id AS request_id,
		t.ts AS ts,
		t.tenant_id::text AS tenant_id,
		(CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id,
		(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) AS api_key_id,
		t.end_user_id AS end_user_id,
		d.client_model AS client_model,
		t.model AS outbound_model,
		(CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id,
		d.provider_id AS provider_id,
		t.canonical_id AS canonical_id,
		d.client_profile AS client_profile,
		t.submit_mode AS request_mode,
		t.prompt_tokens AS prompt_tokens,
		t.completion_tokens AS completion_tokens,
		NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens,
		t.cost_usd::numeric(14,8) AS cost_usd,
		t.latency_ms AS latency_ms,
		t.success AS success,
		t.error_kind AS error_kind,
		t.search_text AS search_text,
		t.cache_read_tokens AS cache_read_tokens,
		t.cache_write_tokens AS cache_write_tokens,
		t.identity_hash AS identity_hash,
		t.virtual_client_id AS virtual_client_id,
		d.virtual_ip AS virtual_ip,
		d.virtual_mac AS virtual_mac,
		d.affinity_hit AS affinity_hit,
		t.stream_first_chunk_ms AS stream_first_chunk_ms,
		t.stream_chunk_count AS stream_chunk_count,
		t.stream_interrupted AS stream_interrupted,
		t.stream_done_sent AS stream_done_sent,
		t.request_checksum AS request_checksum,
		t.response_checksum AS response_checksum,
		d.transform_rule_id AS transform_rule_id,
		t.egress_protocol AS egress_protocol,
		t.failure_stage AS failure_stage,
		t.failure_detail_code AS failure_detail_code,
		t.request_preview AS request_preview,
		t.transform_summary AS transform_summary,
		t.response_preview AS response_preview,
		t.stream_done_sent AS stream_done_received,
		t.cost_display::numeric(14,8) AS cost_display,
		t.cost_currency AS cost_currency,
		t.usage_source AS usage_source,
		(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END) AS gw_session_id,
		d.gw_task_id AS gw_task_id,
		(CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END) AS request_status,
		d.api_key_prefix AS api_key_prefix,
		d.owner_user AS owner_user,
		d.application_code AS application_code,
		d.key_alias AS key_alias,
		d.api_key_owner_user AS api_key_owner_user,
		t.is_auto_request AS is_auto_request,
		t.task_type AS task_type,
		d.auto_profile AS auto_profile,
		(CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END) AS auto_decision,
		t.auto_confidence::numeric(4,3) AS auto_confidence,
		t.work_type AS work_type,
		t.task_type_chosen AS task_type_chosen,
		d.confidence_num AS confidence_num,
		d.model_chosen AS model_chosen,
		d.strategy_used AS strategy_used,
		t.credits_charged AS credits_charged,
		t.parent_request_id AS parent_request_id,
		d.compression_reason AS compression_reason,
		t.compression_strategy AS compression_strategy,
		t.compression_meta AS compression_meta,
		d.outbound_msg_count AS outbound_msg_count,
		d.outbound_token_est AS outbound_token_est,
		d.outbound_msg_hashes AS outbound_msg_hashes,
		d.quality_flags AS quality_flags,
		d.quality_fix_actions AS quality_fix_actions,
		d.quality_score AS quality_score,
		t.upstream_finish_reason AS upstream_finish_reason,
		t.tools AS tool_calls,
		t.client_endpoint AS client_endpoint,
		t.client_timeout AS client_timeout,
		d.stream_chunk_errors AS stream_chunk_errors,
		d.stream_chunks_sent AS stream_chunks_sent,
		t.client_request_id AS client_request_id,
		t.upstream_status_code AS upstream_status_code,
		NULL::text[] AS test_col,
		NULL::text AS test_tab_indent,
		NULL::text AS provider_model,
		d.attachments AS attachments,
		(CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END) AS has_attachments,
		t.attachment_count AS attachment_count,
		t.routing_attempts AS routing_attempts,
		t.routing_summary AS routing_summary,
		t.agent_name AS agent_name,
		t.agent_type AS agent_type,
		t.client_protocol::character varying(50) AS client_protocol,
		t.canonical_model AS canonical_model,
		t.t0_arrived_at AS t0_arrived_at,
		t.t1_total_enqueued_at AS t1_total_enqueued_at,
		t.t2_total_dequeued_at AS t2_total_dequeued_at,
		t.t3_model_enqueued_at AS t3_model_enqueued_at,
		t.t4_model_dequeued_at AS t4_model_dequeued_at,
		t.t5_cred_enqueued_at AS t5_cred_enqueued_at,
		t.t6_cred_dequeued_at AS t6_cred_dequeued_at,
		t.t7_forward_start_at AS t7_forward_start_at,
		t.t8_response_start_at AS t8_response_start_at,
		t.t9_response_end_at AS t9_response_end_at,
		d.request_type AS request_type,
		t.is_final_success AS is_final_success,
		t.origin_actor::character varying(255) AS origin_actor,
		t.customer_id AS customer_id,
		d.request_class AS request_class,
		d.due_at AS due_at,
		t.system_fingerprint AS system_fingerprint,
		t.raw_model_name AS raw_model_name,
		NULL::double precision AS credits_rate_multiplier,
		(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END) AS client_ip,
		t.origin_stage AS origin_stage,
		t.token_band AS token_band,
		t.client_forwarded_for AS client_forwarded_for
  $proj$;

  EXECUTE format($ddl$
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
  $ddl$, proj, proj);

  COMMENT ON VIEW public.request_logs_with_current_month IS
    '会话存储解耦 v4（890）: v1 族（_request_logs*）整体退役 ⇒ canonical 视图去掉第三臂'
    '（冻结 v1 体 + 双反连接去重），只剩两臂 session_turns_hot ∪ session_turns。'
    '两臂投影与 710/734/813/815/816/817/824 装出来的逐字相同 ⇒ 118 列同名同序 ⇒ 读方零改动。'
    '仍 NULL：id（v1 请求行 id ≠ session turn id）/test_col/test_tab_indent/provider_model/'
    'credits_rate_multiplier，以及 client_ip 本身（字面量非法时落 NULL，不抛错）。'
    'trace_events 刻意未投影（镜像从不写，近窗非空率 0，投影即净数据损失）。';
END $$;

-- ── 2b. ★ 把 `routing_analytics_source` 换成 **canonical 形态** ──────────────
--
-- ⚠⚠⚠ 这一步是 **2026-10-20 新装链实测抓到的**：**没有它，890 在新装路径上直接中止**
--   （实测报错：`cannot drop table _request_logs_hot because other objects depend on it`，
--   出处：installer 的 fresh-install 链门 `TestStartupChainGapsMatchReviewedSet`）。
--
-- 缺陷本体（三层）：
--   ① `routing_analytics_source` 由**迁移 649** 创建，body 是
--      `FROM public.request_logs_hot UNION ALL …`（**直读 v1**）；
--   ② **生产**上它在 2026-10-13（S3 波 2 停写切换）被**应用启动期**的
--      `CREATE OR REPLACE VIEW` 换成了 `FROM request_logs_with_current_month`
--      （`db/db.go` 的 `routingAnalyticsMVSQL`）⇒ 升级路径上它早就不依赖 v1；
--   ③ **新装路径**上应用**从没启动过**（安装器只跑迁移，不跑二进制）
--      ⇒ 它仍是 649 的 v1 直读形态 ⇒ 890 步骤 5 的 `DROP TABLE`
--      （**刻意不用 CASCADE**，见该步注释）被它挡住 ⇒ **整个新装被 890 中止**。
--
-- 修法：把它换成**与生产逐字相同**的 canonical 形态（15 列，列名/类型/次序
--   与旧体逐位一致 ⇒ `CREATE OR REPLACE` 的列契约成立）：
--   · **生产**上这一步是幂等 no-op（body 已相同）；
--   · **新装**上它切断 v1 依赖边 ⇒ 步骤 5 的删表不再被挡。
--
-- ⚠ 为什么**不能**靠「应用启动时自愈」兜底：生产的推荐顺序是
--   **先上新二进制（890 延后）再落 schema**（见文件头与 runbook）⇒ 那时应用
--   **已经启动过、早于 890** ⇒ 它的自愈**不会**在 890 之后再跑一次
--   ⇒ 视图会停留在 v1 形态 ⇒ 删表照样被挡。
--   ⇒ **依赖边必须由迁移自己切断**，不能指望「下一个会来的进程」。
--
-- ⚠ 649 是**历史迁移**，其字节不可变（sha 台账 + 「历史 SQL 字节不可变」原则）
--   ⇒ 这里只能在**退役迁移**里修。这也是本轮第 3 次撞到「基线/历史形态 ≠ 生产形态」
--   （前两次：5 张分析视图、2 张中层包装视图）。
DO $$
BEGIN
  IF to_regclass('public.routing_analytics_source') IS NOT NULL THEN
    EXECUTE $ddl$
      CREATE OR REPLACE VIEW public.routing_analytics_source AS
      SELECT
        ts,
        task_type::text AS task_type,
        outbound_model::text AS outbound_model,
        client_model::text AS client_model,
        work_type::text AS work_type,
        provider_id::bigint AS provider_id,
        credential_id::bigint AS credential_id,
        is_auto_request::boolean AS is_auto_request,
        tenant_id::text AS tenant_id,
        request_id::text AS request_id,
        success::boolean AS success,
        latency_ms::numeric AS latency_ms,
        cost_usd::numeric AS cost_usd,
        origin_stage::text AS origin_stage,
        auto_profile::text AS auto_profile
      FROM public.request_logs_with_current_month
    $ddl$;
  END IF;
END $$;

-- ── 2c. ★★ 把两条 `*_owner_filter`（RESTRICTIVE）策略改写成读 `sessions.owner_user` ──
--
-- ⚠⚠⚠ 这是**退役的硬前置**，不是「退役之后可以再看的事」。
--   仓库里早有权威决策：`docs/audit/2026-10-04-session-migration-decision-sheet.md`
--   的 **D9**（并援引审计 §9.137 / §9.139 的真库实测）：
--     整改路径三步、**顺序不能换**：
--       ① `sessions.owner_user` 已存在（§9.141.1）⇒ 不需要新增字段；
--       ② **把 `*_owner_filter` 策略改写成读 `sessions.owner_user`，不再引用 `request_logs`**；
--       ③ **最后才 `DROP TABLE request_logs`**。
--   而 §9.139 的实测已经给出失败形态：
--     「`DROP TABLE request_logs`（策略仍在）⇒ **ERROR: cannot drop table … because
--     other objects depend on it** / DETAIL: policy … depends on table …」
--   ⇒ 只做 ③ 会被 PostgreSQL **直接拦下** ⇒ 整条启动链中止 ⇒ **新装被 890 中止**。
--   （2026-10-20 新装链实测复现了这条报错，本步就是它的修法。）
--
-- 缺陷本体：`session_turns` / `session_turns_hot` 上的两条 RESTRICTIVE 策略
--   （457 建、526 给 hot 表补上）用
--     `SELECT DISTINCT ON (gw_session_id, tenant_id) … FROM _request_logs_hot
--       UNION ALL … FROM _request_logs … ORDER BY ts`
--   取**最早那行的 owner_user** 来判「这一行归谁可见」
--   ⇒ 策略表达式**内部**引用 v1 族 ⇒ v1 一删，策略就变成**每次行访问都 42P01**。
--
-- 修法：把 owner 的解析源从 v1 族换成**会话族自己的** `sessions.owner_user`：
--   `sessions.session_id`（text）与 `session_turns.session_id`（text）**同型** ⇒
--   可直接等值连接；`sessions.owner_user` 是 text，`current_setting('app.current_user', true)`
--   也是 text ⇒ 同型可比。
--
-- ⚠ **安全语义没有被悄悄改变**（逐条核对）：
--   · 改写**前**（现状）：owner 来自 v1 族，而 v1 族自 2026-10-13 停写后**恒空**
--     ⇒ `EXISTS(...)` 恒假 ⇒ **对非超管一律不可见**（fail-closed）。
--   · 改写**后**：owner 来自 `sessions.owner_user`；而 `app.current_user`
--     在**生产里零设置点**（审计 R38 `agent-guc-surface.md` 实测：仅测试 set_config）
--     ⇒ `current_setting('app.current_user', true)` 返回 NULL ⇒
--     `s.owner_user = NULL` ⇒ NULL ⇒ `EXISTS` 恒假 ⇒ **仍然 fail-closed**。
--   ⇒ 在今天的部署形态下**可见性完全不变**；本步只把「owner 的来源」从
--     一张**即将被删的表**换成会话族自己 ⇒ 依赖边消失、语义不变。
--   · 超管旁路（`app.current_role='super_admin'` / `app.bypass_rls='true'）**原样保留**。
--
-- ⚠ 只在策略**存在时**才改写（`IF EXISTS`）：
--   · **生产**上这两条策略**已经不在**（实测本地真库：0 条 owner_filter 策略）⇒ 本步是 no-op；
--   · **新装**上它们在（457/526 建）⇒ 本步把它们从 v1 依赖切到会话族。
--   ⇒ 不在策略不存在时**凭空造**一条（那会把生产变成更严的形态 ⇒ 环境分叉）。
--
-- ⚠ down 侧的对称复原见 down 文件的「v1 owner 过滤策略复原」节：
--   只在「当前策略是本步改写后的形态」时才还原成 v1 形态 ⇒ 生产（本来就没有）仍是 no-op。
DO $$
DECLARE
  v_exists boolean;
BEGIN
  -- session_turns ────────────────────────────────────────────────────────
  SELECT EXISTS (
    SELECT 1 FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relname = 'session_turns'
       AND p.polname = 'session_turns_owner_filter'
  ) INTO v_exists;
  IF v_exists THEN
    EXECUTE $ddl$
      DROP POLICY IF EXISTS session_turns_owner_filter ON public.session_turns;
      CREATE POLICY session_turns_owner_filter ON public.session_turns
        AS RESTRICTIVE
        FOR ALL
        TO PUBLIC
        USING (
          (current_setting('app.current_role', true) = 'super_admin')
          OR (current_setting('app.bypass_rls', true) = 'true')
          OR EXISTS (
            SELECT 1
              FROM public.sessions s
             WHERE s.session_id = public.session_turns.session_id
               AND s.owner_user = current_setting('app.current_user', true)
          )
        );
    $ddl$;
  END IF;

  -- session_turns_hot ───────────────────────────────────────────────────
  SELECT EXISTS (
    SELECT 1 FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relname = 'session_turns_hot'
       AND p.polname = 'session_turns_hot_owner_filter'
  ) INTO v_exists;
  IF v_exists THEN
    EXECUTE $ddl$
      DROP POLICY IF EXISTS session_turns_hot_owner_filter ON public.session_turns_hot;
      CREATE POLICY session_turns_hot_owner_filter ON public.session_turns_hot
        AS RESTRICTIVE
        FOR ALL
        TO PUBLIC
        USING (
          (current_setting('app.current_role', true) = 'super_admin')
          OR (current_setting('app.bypass_rls', true) = 'true')
          OR EXISTS (
            SELECT 1
              FROM public.sessions s
             WHERE s.session_id = public.session_turns_hot.session_id
               AND s.owner_user = current_setting('app.current_user', true)
          )
        );
    $ddl$;
  END IF;
END $$;

-- ── 3. ★ 把**两个仍然静态读 v1 的函数**换成会话族形态 ────────────────────────
--
-- ⚠⚠ 为什么必须在这一步、而不是留给二进制启动时自愈：
--   · `recent_success_rate` 被 provider/client.go 的候选查询以
--     `CROSS JOIN LATERAL` 调用 ⇒ v1 腿 42P01 ⇒ **整条候选查询失败 ⇒
--     选不出任何上游凭据 ⇒ 100% 流量失败**。
--   · 而 `CREATE OR REPLACE FUNCTION` 对 SQL 函数体**不在创建时解析**
--     ⇒ 函数能建成功、直到第一次被调用才炸 ⇒ 症状与"上游全挂"同形，
--     根因却在一次字符串手术上。
--   ⇒ 删表前先把函数换掉。（db/db.go 的 recentSuccessRateEnsureSQL 是同一段
--     文本的运行时 SSOT，二者逐字一致由 db/request_logs_view_schema_test.go
--     的「迁移函数体 ⇔ Go SSOT」断言钉住。）
--
-- ⚠ `system_health_status` 同理：它 30s 窗口读 `_request_logs_hot` ⇒ 42P01
--   ⇒ 健康徽章永久 "suspect" + 每 30s 一条 WARN。换成 `session_turns_hot`
--   （会话族的"热窗"表，语义与 v1 hot 对位）。
-- ⚠ 引用标签的坑：外层 DO 块**不能**用 `$$`——两个函数体内部各自用 `$$`
--   （SQL 函数体的惯用引用符），会**提前闭合**外层块 ⇒ 语法错误在 "WITH"。
--   ⇒ 外层用 `$blk$`，内层用 `$ddl$` / `$$` / `$function$`，互不撞车。
DO $blk$
BEGIN
  EXECUTE $ddl$
    CREATE OR REPLACE FUNCTION recent_success_rate(p_credential_id BIGINT,
                                        p_raw_model     TEXT,
                                        p_sample_n      INT DEFAULT 50,
                                        p_window_hours  INT DEFAULT 3)
    RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
    LANGUAGE sql
    STABLE
    AS $$
            WITH recent AS (
                SELECT success, ts
                    FROM public.session_turns_hot
                    WHERE credential_id ~ '^[0-9]+$'
                      AND credential_id::bigint = p_credential_id
                      AND lower(COALESCE(model, canonical_model)) = lower(p_raw_model)
                      AND ts > NOW() - (p_window_hours || ' hours')::interval
                      AND COALESCE(task_type, '') <> 'probe_triggered'
                      AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
                      AND request_id NOT LIKE 'probe-%'
            )

        SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
               COUNT(*)::int
        FROM (
            SELECT success FROM recent ORDER BY ts DESC LIMIT p_sample_n
        ) newest_samples;
    $$
  $ddl$;

  EXECUTE $ddl$
    CREATE OR REPLACE FUNCTION public.system_health_status(p_window_seconds integer DEFAULT 30)
    RETURNS TABLE(status text, success_rate numeric, sample_count bigint, failure_count bigint, last_check_at timestamp with time zone)
    LANGUAGE sql
    STABLE
    AS $function$
        WITH win AS (
            SELECT
                COUNT(*)::bigint                AS n,
                COUNT(*) FILTER (WHERE success)::bigint AS ok,
                COUNT(*) FILTER (WHERE NOT success)::bigint AS fail
            FROM public.session_turns_hot
            WHERE ts >= now() - make_interval(secs => p_window_seconds)
        )
        SELECT
            CASE
                WHEN n = 0                                  THEN 'suspect'
                WHEN (ok::numeric / NULLIF(n,0)) >= 0.80    THEN 'ok'
                ELSE 'degraded'
            END                                            AS status,
            ROUND( (ok::numeric / NULLIF(n,0))::numeric, 4) AS success_rate,
            n                                              AS sample_count,
            fail                                           AS failure_count,
            now()                                          AS last_check_at
        FROM win;
    $function$
  $ddl$;
END $blk$;

-- ── 4. 删 v1-only 视图 ────────────────────────────────────────────────────
-- 顺序：先下游（…_without_request_class_due_at）后上游（…_without_customer_id）。
--
-- ⚠ 这些**视图名**不会被部署通道改写（改写正则只认 `request_logs` 后面跟
--   `_bodies_hot|_bodies|_hot|_archive|_id_seq` 的整词，视图名里 `request_logs`
--   后面跟的是 `_with_…` ⇒ 整词不成立 ⇒ 不改写）⇒ 这里写原名是对的。
--   而下面的**表名**会被改写 ⇒ 表名按两种拼写都试。
DO $$
DECLARE
  v_name text;
BEGIN
  FOREACH v_name IN ARRAY ARRAY[
      'request_logs_with_current_month_without_request_class_due_at',
      'request_logs_with_current_month_without_customer_id',
      'request_logs_bodies_with_current_month',
      'request_logs_bodies_progress'
  ] LOOP
    IF to_regclass('public.' || v_name) IS NOT NULL THEN
      EXECUTE format('DROP VIEW IF EXISTS public.%I', v_name);
    END IF;
  END LOOP;
END $$;

-- ── 5. 删 v1 族表（母表带分区一起删；hot 表与序列单独删）──────────────────
-- ⚠ 不用 CASCADE：若出现**预期外**的依赖者，让它**响亮失败**而不是被顺手删掉。
--   本文件的依赖顺序已保证那 4 张视图先被删（步骤 3）。
DO $$
DECLARE
  v_name text;
BEGIN
  FOREACH v_name IN ARRAY ARRAY[
      'request_logs_bodies_hot', '_request_logs_bodies_hot',
      'request_logs_hot', '_request_logs_hot',
      'request_logs_bodies', '_request_logs_bodies',
      'request_logs_archive', '_request_logs_archive',
      'request_logs', '_request_logs',
      'request_logs_id_seq', '_request_logs_id_seq'
  ] LOOP
    IF to_regclass('public.' || v_name) IS NOT NULL THEN
      IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                  WHERE n.nspname = 'public' AND c.relname = v_name AND c.relkind = 'S') THEN
        EXECUTE format('DROP SEQUENCE IF EXISTS public.%I', v_name);
      ELSE
        EXECUTE format('DROP TABLE IF EXISTS public.%I', v_name);
      END IF;
    END IF;
  END LOOP;
END $$;

-- ── 6. 删只服务 v1 族的函数 ────────────────────────────────────────────────
-- 分区管理族：它们的存在前提是 v1 族在。族没了，它们要么 42P01、要么静默空转。
-- ⇒ 一并删掉，避免"看起来还在管、其实管的是空气"。
DROP FUNCTION IF EXISTS public.ensure_request_logs_partition(timestamptz);
DROP FUNCTION IF EXISTS public.ensure_request_logs_bodies_partition(timestamptz);
DROP FUNCTION IF EXISTS public.promote_request_logs_hot_to_partition(interval, int);
DROP FUNCTION IF EXISTS public.promote_request_logs_bodies_hot_to_partition(interval, int);
DROP FUNCTION IF EXISTS public.promote_request_logs_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.promote_request_logs_bodies_default_batch(interval, int);
DROP FUNCTION IF EXISTS public.create_next_month_partitions();
DROP FUNCTION IF EXISTS public.ensure_next_month_archive_partition();
DROP FUNCTION IF EXISTS public.archive_request_logs(date);
DROP FUNCTION IF EXISTS public.archive_request_logs_default(int);
DROP FUNCTION IF EXISTS public.repair_request_logs_detached_partitions();
DROP PROCEDURE IF EXISTS public.backfill_request_logs_bodies(integer);
DROP FUNCTION IF EXISTS public.cleanup_stale_in_progress_requests();
DROP FUNCTION IF EXISTS public.credential_most_used_model(bigint, int);
DROP FUNCTION IF EXISTS public.credential_most_used_model(int, int);
DROP FUNCTION IF EXISTS public.get_last_successful_request(varchar, int);
DROP FUNCTION IF EXISTS public.update_api_key_model_cost();
DROP FUNCTION IF EXISTS public.drop_old_request_logs_bodies_partitions(int);

-- 两个触发器函数：触发器随表自动消失，函数会变孤儿 ⇒ 显式删。
DROP FUNCTION IF EXISTS public.update_session_tenant_binding();
DROP FUNCTION IF EXISTS public.update_session_summary();

-- Ledger self-registration（710 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('890', 'v1 request_logs family retired for real: canonical view reduced to the session-only two-arm body, v1-only views and the whole _request_logs* family (tables, partitions, sequence) dropped, v1-only partition-management functions removed')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
