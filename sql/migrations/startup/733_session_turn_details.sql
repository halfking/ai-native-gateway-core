-- Migration 733: 会话存储解耦 v3 —— session_turn_details 特征层（Full 链路）
--
-- docs/storage/2026-09-20-session-storage-decoupling-plan.md §3：
-- 四表分层 sessions(聚合) / session_turns(元数据瘦核心) /
-- session_turn_details(特征 1:1 懒加载) / session_bodies(原文)。
--
-- 本迁移建 details 表族（hot + 月分区；不建 default 分区，见 §3）：
--   · 视图契约组 30 列：710 canonical 视图 session 分支的 NULL 占位列
--     （client_model / quality_* / stream_chunk_errors / request_class 等），
--     734 起 LEFT JOIN 本表替换占位，管理端/回放不再回源 request_logs。
--   · 分析储备组 9 列：v_node_switch_analysis / v_timeout_effectiveness /
--     v_continuation_effectiveness 三视图待迁列（S3）。
--   · 多维 token / 安全 / 上游组 15 列。
-- 回填：request_logs(hot UNION parent) 特征 JOIN session_turns(hot UNION
-- parent) 反守卫，50000/批循环防长事务；幂等可重跑（候选必须含
-- session_turns_hot——热表里尚未 promote 的近期 turns 同样缺 details 行，
-- writer 只覆盖新 turn，错过本批即永久缺行）。源列两级守卫：视图契约组
-- 30 列硬引用（冻结 113 列契约成员必有）；储备/多维组 24 列按
-- information_schema 交集回退 NULL（冻结契约外的物理附加列，陈旧库可能
-- 缺列，不逼停部署通道）。telemetry.RequestLogEntry 缺源列
-- （node_switch_count 等 2026-09-20 审计确认零匹配）照建、暂 NULL，
-- 沿用 707 §9 NULL 补位登记惯例。
--
-- promote：promote_session_turn_details_hot_to_partition 镜像 707 形态
-- （列集合契约校验 + 目录派生列清单 + advisory lock + ensure partition）。
--
-- Compatibility: 纯新增表族，零既有表变更。
-- Down: DROP 表族/函数/序列（734 视图依赖本表，down 按号逆序先 734 后 733）。

BEGIN;

-- =============================================
-- 1. 序列 + 父表（月分区）
-- =============================================

CREATE SEQUENCE IF NOT EXISTS public.session_turn_details_id_seq;

CREATE TABLE IF NOT EXISTS public.session_turn_details (
    id bigint DEFAULT nextval('public.session_turn_details_id_seq') NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,

    -- 视图契约组（710 NULL 占位 → d.col）
    client_model text,
    provider_id bigint,
    client_profile text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    transform_rule_id text,
    gw_task_id text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    auto_profile text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    compression_reason text,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[],
    quality_fix_actions jsonb,
    quality_score numeric(3,2),
    stream_chunk_errors integer,
    stream_chunks_sent integer,
    attachments jsonb,
    request_type text,
    request_class text,
    due_at timestamptz,

    -- 分析储备组（S3 三视图待迁）
    node_switch_count integer DEFAULT 0,
    timeout_mode character varying(50),
    effective_timeout_seconds integer,
    is_continuation boolean DEFAULT false,
    cached_response_id bigint,
    context_size_tokens integer,
    keepalive_sent_count integer DEFAULT 0,
    cache_hit boolean,
    cache_tokens_saved integer,

    -- 多维 token / 安全 / 上游组
    reasoning_tokens integer,
    image_tokens integer,
    audio_tokens integer,
    video_tokens integer,
    provider_tokens integer,
    protocol_conversion boolean,
    rate_limit_status character varying(50),
    content_safety_score jsonb,
    dlp_violations jsonb,
    sensitive_keywords text[],
    upstream_endpoint text,
    task_id character varying(255),
    task_title text,
    api_key_fingerprint character varying(16),
    provider_model text,

    CONSTRAINT session_turn_details_pkey PRIMARY KEY (partition_date, id),
    CONSTRAINT session_turn_details_request_id_key UNIQUE (tenant_id, request_id, partition_date),
    CONSTRAINT session_turn_details_session_turn_key UNIQUE (session_id, turn_no, partition_date)
)
PARTITION BY RANGE (partition_date);

COMMENT ON TABLE public.session_turn_details IS
    'V3 特征层：与 session_turns 1:1（tenant_id+request_id+partition_date），'
    '承载 710 视图 NULL 占位列 + 分析/安全/多维 token 特征，懒加载不进热路径。'
    '元数据在 session_turns，原文在 session_bodies。Created: 2026-09-20, Migration 733';

-- =============================================
-- 2. hot 表（heap，与父表同形）
-- =============================================

CREATE TABLE IF NOT EXISTS public.session_turn_details_hot (
    id bigint DEFAULT nextval('public.session_turn_details_id_seq') NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL,
    client_model text,
    provider_id bigint,
    client_profile text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    transform_rule_id text,
    gw_task_id text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    auto_profile text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    compression_reason text,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[],
    quality_fix_actions jsonb,
    quality_score numeric(3,2),
    stream_chunk_errors integer,
    stream_chunks_sent integer,
    attachments jsonb,
    request_type text,
    request_class text,
    due_at timestamptz,
    node_switch_count integer DEFAULT 0,
    timeout_mode character varying(50),
    effective_timeout_seconds integer,
    is_continuation boolean DEFAULT false,
    cached_response_id bigint,
    context_size_tokens integer,
    keepalive_sent_count integer DEFAULT 0,
    cache_hit boolean,
    cache_tokens_saved integer,
    reasoning_tokens integer,
    image_tokens integer,
    audio_tokens integer,
    video_tokens integer,
    provider_tokens integer,
    protocol_conversion boolean,
    rate_limit_status character varying(50),
    content_safety_score jsonb,
    dlp_violations jsonb,
    sensitive_keywords text[],
    upstream_endpoint text,
    task_id character varying(255),
    task_title text,
    api_key_fingerprint character varying(16),
    provider_model text,
    CONSTRAINT session_turn_details_hot_pkey PRIMARY KEY (id),
    CONSTRAINT session_turn_details_hot_request_key UNIQUE (tenant_id, request_id, partition_date),
    CONSTRAINT session_turn_details_hot_session_turn_key UNIQUE (session_id, turn_no, partition_date)
)
WITH (autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05',
      autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02',
      autovacuum_analyze_threshold='50');

COMMENT ON TABLE public.session_turn_details_hot IS
    'session_turn_details 的 hot 层：writer 只写这里，promote 冷却后搬月分区。'
    'Created: 2026-09-20, Migration 733';

-- 视图 734 的 LEFT JOIN 键 + writer upsert 键
CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_request
    ON public.session_turn_details_hot (request_id, partition_date);
CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_session
    ON public.session_turn_details_hot (tenant_id, session_id, turn_no);
CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_ts
    ON public.session_turn_details_hot (ts);

-- =============================================
-- 3. 分区 ensure（回填历史 partition_date 必需；不建 default 分区）
-- =============================================

CREATE OR REPLACE FUNCTION public.ensure_session_turn_details_partition(target_date date DEFAULT CURRENT_DATE)
    RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    partition_suffix TEXT;
BEGIN
    partition_suffix := to_char(target_date, 'YYYY_MM');
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS public.session_turn_details_%s PARTITION OF public.session_turn_details
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix,
        date_trunc('month', target_date),
        date_trunc('month', target_date + INTERVAL '1 month')
    );
END;
$$;

-- 当前与次月分区预建（不建 default：promote/回填先 ensure 批次月份再插入，
-- 镜像 707/513 形态；default 会与后续月分区创建互斥）
SELECT public.ensure_session_turn_details_partition(CURRENT_DATE);
SELECT public.ensure_session_turn_details_partition((CURRENT_DATE + INTERVAL '1 month')::date);

-- =============================================
-- 4. RLS（镜像 session_turns 词表：tenant_isolation + super_admin_bypass）
-- =============================================

ALTER TABLE public.session_turn_details ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_turn_details_hot ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    DROP POLICY IF EXISTS session_turn_details_tenant_isolation ON public.session_turn_details;
    CREATE POLICY session_turn_details_tenant_isolation ON public.session_turn_details
        USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));
    DROP POLICY IF EXISTS session_turn_details_super_admin_bypass ON public.session_turn_details;
    CREATE POLICY session_turn_details_super_admin_bypass ON public.session_turn_details
        USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text)
            OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));
    DROP POLICY IF EXISTS session_turn_details_hot_tenant_isolation ON public.session_turn_details_hot;
    CREATE POLICY session_turn_details_hot_tenant_isolation ON public.session_turn_details_hot
        USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));
    DROP POLICY IF EXISTS session_turn_details_hot_super_admin_bypass ON public.session_turn_details_hot;
    CREATE POLICY session_turn_details_hot_super_admin_bypass ON public.session_turn_details_hot
        USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text)
            OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));
END $$;

-- =============================================
-- 5. promote：hot → 月分区（镜像 707 契约形态）
--    R51 (2026-09-21)：INSERT 侧 ON CONFLICT DO NOTHING → DO UPDATE。
--    回填（同文件第 6 节）与 promote 并发时：若回填在候选选定后、INSERT
--    前把同一 (tenant_id, request_id, partition_date) 行写入 parent，
--    DO NOTHING 会「hot 行已 DELETE、parent 插入被跳过」→ 新值静默丢失。
--    DO UPDATE 以 hot 行新值覆盖（last-writer-wins，无丢行；回填源
--    request_logs 不删行，方向相反时也只是覆盖不丢失）。函数内
--    advisory lock 只串行化 promote 实例自身，管不到回填 DO 块——
--    正是靠 DO UPDATE 兜住跨通道并发。
-- =============================================

CREATE OR REPLACE FUNCTION public.promote_session_turn_details_hot_to_partition(
    p_retention INTERVAL DEFAULT '8 hours',
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved BIGINT := 0;
    v_batch BIGINT := 0;
    v_partition_date DATE;
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
    v_set TEXT;
BEGIN
    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be a positive interval';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
    END IF;

    -- 列集合契约：父/hot（列名:类型:非空）全等，漂移即刻报错不静默丢列。
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION 'session_turn_details hot/parent column contract has drifted';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
      INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;

    -- DO UPDATE 的 SET 列清单（R51）：排除 id（主键 (partition_date, id)
    -- 的身份列，不随搬移回写）与 partition_date（冲突目标键，恒等值）。
    SELECT string_agg(quote_ident(attname) || ' = EXCLUDED.' || quote_ident(attname), ', ' ORDER BY attnum)
      INTO v_set
      FROM pg_attribute
     WHERE attrelid = 'public.session_turn_details_hot'::regclass
       AND attnum > 0 AND NOT attisdropped
       AND attname NOT IN ('id', 'partition_date');

    PERFORM pg_advisory_xact_lock(
        hashtextextended('public.promote_session_turn_details_hot_to_partition', 0)
    );

    LOOP
        -- 批次涉及月份先 ensure（default 不存在，分区必须在 INSERT 前就位）
        FOR v_partition_date IN
            SELECT DISTINCT h.partition_date
            FROM public.session_turn_details_hot h
            WHERE h.ts < statement_timestamp() - p_retention
              AND NOT EXISTS (
                  SELECT 1 FROM public.session_turn_details d
                  WHERE d.tenant_id = h.tenant_id
                    AND d.request_id = h.request_id
                    AND d.partition_date = h.partition_date
              )
        LOOP
            PERFORM public.ensure_session_turn_details_partition(v_partition_date);
        END LOOP;

        -- 目录派生列清单 + 按位置同序 INSERT（父/hot 本迁移同形创建；契约
        -- 校验保证集合全等，列序敏感由本迁移同形 DDL 保证）。
        -- R51：DO UPDATE（目标 = request_id 唯一约束）覆盖回填并发写入的
        -- 行——hot 新值胜出，DELETE 掉的 hot 行不丢（DO NOTHING 会丢）。
        EXECUTE format(
            'WITH candidate AS (
                SELECT h.id
                FROM public.session_turn_details_hot h
                WHERE h.ts < statement_timestamp() - %L::interval
                  AND NOT EXISTS (
                      SELECT 1 FROM public.session_turn_details d
                      WHERE d.tenant_id = h.tenant_id
                        AND d.request_id = h.request_id
                        AND d.partition_date = h.partition_date
                  )
                ORDER BY h.ts, h.id
                LIMIT %s
                FOR UPDATE SKIP LOCKED
            ), moved AS (
                DELETE FROM public.session_turn_details_hot h
                USING candidate c
                WHERE h.id = c.id
                RETURNING h.*
            ), inserted AS (
                INSERT INTO public.session_turn_details (%s)
                SELECT %s FROM moved
                ON CONFLICT (tenant_id, request_id, partition_date) DO UPDATE SET %s
                RETURNING 1
            )
            SELECT count(*) FROM inserted',
            p_retention, p_batch_size, v_cols, v_cols, v_set)
        INTO v_batch;
        EXIT WHEN v_batch = 0;
        v_moved := v_moved + v_batch;
    END LOOP;

    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION public.promote_session_turn_details_hot_to_partition(INTERVAL, INTEGER) IS
    'Move cold rows from session_turn_details_hot to monthly partitions (733,
     707-mirror contract: set-equality column check + catalog-derived list).';

-- =============================================
-- 6. 回填：request_logs(hot UNION parent) → details（批量循环）
--    目标行 = 已镜像的 turns（session_turns_hot ∪ session_turns：热表里
--    尚未 promote 的近期 turns 也是存量缺口，writer 只覆盖部署后的新
--    turn）；反守卫幂等。
--    源列两级守卫：
--      · 视图契约组 30 列 = 冻结 113 列契约成员，request_logs 必有，硬引用；
--      · 储备组/多维组 24 列属冻结契约外的物理附加列（multimodal token
--        等分批落地），陈旧库/交集克隆库可能缺列——按 information_schema
--        交集回退 NULL::<type>，不逼停部署通道（镜像 710 ensure 家族的
--        陈旧库兼容哲学）。
-- =============================================

DO $$
DECLARE
    v_inserted BIGINT := 0;
    v_total BIGINT := 0;
    v_part DATE;
    v_src_outer TEXT;   -- 外层 SELECT 侧：rl.<col> / NULL::<type>
    v_src_inner TEXT;   -- UNION 内层侧：<col> / NULL::<type>
BEGIN
    -- v1 源表缺失（全新库未建 request_logs）时跳过
    IF to_regclass('public.request_logs_hot') IS NULL
       OR to_regclass('public.request_logs') IS NULL
       OR to_regclass('public.session_turns') IS NULL
       OR to_regclass('public.session_turns_hot') IS NULL THEN
        RAISE NOTICE '733 backfill skipped: source tables absent';
        RETURN;
    END IF;

    -- 24 个冻结契约外附加列的存在性交集（hot ∧ parent 同时有列才直取）；
    -- 类型表 = session_turn_details 对应列类型（UNION/INSERT 按位置匹型）。
    SELECT string_agg(CASE WHEN p.hot_ok AND p.parent_ok
                           THEN 'rl.' || c.col
                           ELSE 'NULL::' || c.typ END, ', ' ORDER BY c.ord),
           string_agg(CASE WHEN p.hot_ok AND p.parent_ok
                           THEN c.col
                           ELSE 'NULL::' || c.typ END, ', ' ORDER BY c.ord)
      INTO v_src_outer, v_src_inner
      FROM (VALUES
        ('node_switch_count','integer',1),
        ('timeout_mode','character varying(50)',2),
        ('effective_timeout_seconds','integer',3),
        ('is_continuation','boolean',4),
        ('cached_response_id','bigint',5),
        ('context_size_tokens','integer',6),
        ('keepalive_sent_count','integer',7),
        ('cache_hit','boolean',8),
        ('cache_tokens_saved','integer',9),
        ('reasoning_tokens','integer',10),
        ('image_tokens','integer',11),
        ('audio_tokens','integer',12),
        ('video_tokens','integer',13),
        ('provider_tokens','integer',14),
        ('protocol_conversion','boolean',15),
        ('rate_limit_status','character varying(50)',16),
        ('content_safety_score','jsonb',17),
        ('dlp_violations','jsonb',18),
        ('sensitive_keywords','text[]',19),
        ('upstream_endpoint','text',20),
        ('task_id','character varying(255)',21),
        ('task_title','text',22),
        ('api_key_fingerprint','character varying(16)',23),
        ('provider_model','text',24)
    ) AS c(col, typ, ord)
    CROSS JOIN LATERAL (
        SELECT
            EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = 'public'
                      AND table_name = 'request_logs_hot'
                      AND column_name = c.col) AS hot_ok,
            EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = 'public'
                      AND table_name = 'request_logs'
                      AND column_name = c.col) AS parent_ok
    ) p;

    -- 视图契约组 30 列（冻结契约成员，必有）拼在最前。
    v_src_outer := 'rl.client_model, rl.provider_id, rl.client_profile, rl.virtual_ip, rl.virtual_mac, '
                || 'rl.affinity_hit, rl.transform_rule_id, rl.gw_task_id, rl.api_key_prefix, rl.owner_user, '
                || 'rl.application_code, rl.key_alias, rl.api_key_owner_user, rl.auto_profile, rl.confidence_num, '
                || 'rl.model_chosen, rl.strategy_used, rl.compression_reason, rl.outbound_msg_count, '
                || 'rl.outbound_token_est, rl.outbound_msg_hashes, rl.quality_flags, rl.quality_fix_actions, '
                || 'rl.quality_score, rl.stream_chunk_errors, rl.stream_chunks_sent, rl.attachments, '
                || 'rl.request_type, rl.request_class, rl.due_at, ' || v_src_outer;
    v_src_inner := 'client_model, provider_id, client_profile, virtual_ip, virtual_mac, '
                || 'affinity_hit, transform_rule_id, gw_task_id, api_key_prefix, owner_user, '
                || 'application_code, key_alias, api_key_owner_user, auto_profile, confidence_num, '
                || 'model_chosen, strategy_used, compression_reason, outbound_msg_count, '
                || 'outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, '
                || 'quality_score, stream_chunk_errors, stream_chunks_sent, attachments, '
                || 'request_type, request_class, due_at, ' || v_src_inner;

    -- 候选月份先 ensure（无 default 分区，INSERT 前分区必须就位）；
    -- 候选月份同样取 turns hot ∪ parent。
    FOR v_part IN
        SELECT DISTINCT u.partition_date
        FROM (
            SELECT partition_date, tenant_id, request_id FROM public.session_turns_hot
            UNION ALL
            SELECT partition_date, tenant_id, request_id FROM public.session_turns
        ) u
        WHERE NOT EXISTS (
            SELECT 1 FROM public.session_turn_details d
            WHERE d.tenant_id = u.tenant_id::text
              AND d.request_id = u.request_id
              AND d.partition_date = u.partition_date
        )
    LOOP
        PERFORM public.ensure_session_turn_details_partition(v_part);
    END LOOP;

    LOOP
        EXECUTE
            'WITH candidate AS (
                SELECT u.session_id, u.turn_no, u.tenant_id, u.request_id, u.ts, u.partition_date
                FROM (
                    SELECT t.session_id, t.turn_no, t.tenant_id::text AS tenant_id,
                           t.request_id, t.ts, t.partition_date
                    FROM public.session_turns_hot t
                    UNION ALL
                    SELECT t.session_id, t.turn_no, t.tenant_id::text AS tenant_id,
                           t.request_id, t.ts, t.partition_date
                    FROM public.session_turns t
                ) u
                WHERE NOT EXISTS (
                    SELECT 1 FROM public.session_turn_details d
                    WHERE d.tenant_id = u.tenant_id
                      AND d.request_id = u.request_id
                      AND d.partition_date = u.partition_date
                )
                LIMIT 50000
            ),
            inserted AS (
                INSERT INTO public.session_turn_details (
                    session_id, turn_no, tenant_id, request_id, ts, partition_date,
                    client_model, provider_id, client_profile, virtual_ip, virtual_mac,
                    affinity_hit, transform_rule_id, gw_task_id, api_key_prefix, owner_user,
                    application_code, key_alias, api_key_owner_user, auto_profile, confidence_num,
                    model_chosen, strategy_used, compression_reason, outbound_msg_count,
                    outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions,
                    quality_score, stream_chunk_errors, stream_chunks_sent, attachments,
                    request_type, request_class, due_at,
                    node_switch_count, timeout_mode, effective_timeout_seconds, is_continuation,
                    cached_response_id, context_size_tokens, keepalive_sent_count, cache_hit,
                    cache_tokens_saved,
                    reasoning_tokens, image_tokens, audio_tokens, video_tokens, provider_tokens,
                    protocol_conversion, rate_limit_status, content_safety_score, dlp_violations,
                    sensitive_keywords, upstream_endpoint, task_id, task_title,
                    api_key_fingerprint, provider_model
                )
                SELECT c.session_id, c.turn_no, c.tenant_id, c.request_id, c.ts, c.partition_date,
                       ' || v_src_outer || '
                FROM candidate c
                LEFT JOIN (
                    SELECT request_id, ts, ' || v_src_inner || '
                    FROM public.request_logs_hot
                    UNION ALL
                    SELECT request_id, ts, ' || v_src_inner || '
                    FROM public.request_logs
                ) rl ON rl.request_id = c.request_id
                ON CONFLICT DO NOTHING
                RETURNING 1
            )
            SELECT count(*) FROM inserted'
        INTO v_inserted;
        v_total := v_total + v_inserted;
        EXIT WHEN v_inserted = 0;
    END LOOP;

    RAISE NOTICE '733 backfill inserted % session_turn_details rows', v_total;
END $$;

COMMIT;
