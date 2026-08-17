-- Migration 534: handoff_logs independent heap hot table + columnar history.
--
-- The migration is safe to replay on:
--   * the legacy heap-only handoff_logs schema;
--   * the already-upgraded shared 252 database;
--   * a fresh database created from the legacy baseline.
--
-- Application writes target handoff_logs_hot. Reads use the explicit-column
-- handoff_logs_with_current_month view. Historical rows are promoted into the
-- RANGE(created_at) parent, whose monthly partitions use Citus columnar.
\set ON_ERROR_STOP on

BEGIN;
SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:handoff-logs:hot-columnar:v1', 0));

DO $do$
DECLARE
    rel_kind "char";
    rel_am text;
    legacy_name text := 'handoff_logs_legacy_532';
BEGIN
    SELECT c.relkind, a.amname INTO rel_kind, rel_am
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_am a ON a.oid = c.relam
    WHERE n.nspname = 'public' AND c.relname = 'handoff_logs';

    IF rel_kind = 'r' AND rel_am = 'heap' THEN
        IF to_regclass('public.' || legacy_name) IS NOT NULL THEN
            RAISE EXCEPTION 'legacy handoff table % already exists; refusing ambiguous upgrade', legacy_name;
        END IF;
        EXECUTE 'ALTER TABLE public.handoff_logs RENAME TO ' || quote_ident(legacy_name);
    ELSIF rel_kind IS NOT NULL AND rel_kind <> 'p' THEN
        RAISE EXCEPTION 'public.handoff_logs must be a heap table or partitioned parent, relkind=% am=%', rel_kind, rel_am;
    END IF;
END
$do$;

CREATE SEQUENCE IF NOT EXISTS public.handoff_logs_id_seq
    AS integer START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE IF NOT EXISTS public.handoff_logs_hot (
    id integer NOT NULL DEFAULT nextval('public.handoff_logs_id_seq'::regclass),
    session_id varchar(64) NOT NULL,
    tenant_id varchar(64) NOT NULL,
    trigger_reason varchar(64) NOT NULL,
    tokens_at_handoff integer NOT NULL,
    context_window integer,
    handoff_prompt text,
    new_session_id varchar(64),
    created_at timestamp without time zone DEFAULT now(),
    summary_text text,
    summary_engine varchar(32),
    trigger_mode varchar(32),
    tokens_in_session integer,
    messages_in_session integer,
    skill_name varchar(64),
    duration_ms integer,
    CONSTRAINT handoff_logs_hot_pkey PRIMARY KEY (id)
) WITH (fillfactor=90);

ALTER SEQUENCE public.handoff_logs_id_seq OWNED BY NONE;
ALTER TABLE public.handoff_logs_hot
    ALTER COLUMN id SET DEFAULT nextval('public.handoff_logs_id_seq'::regclass);
ALTER SEQUENCE public.handoff_logs_id_seq OWNED BY public.handoff_logs_hot.id;

CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_created_at
    ON public.handoff_logs_hot (created_at, id);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_session
    ON public.handoff_logs_hot (session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_tenant
    ON public.handoff_logs_hot (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_trigger_mode
    ON public.handoff_logs_hot (trigger_reason, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_new_session
    ON public.handoff_logs_hot (new_session_id, created_at DESC)
    WHERE new_session_id IS NOT NULL;

DO $do$
DECLARE
    parent_kind "char";
BEGIN
    SELECT c.relkind INTO parent_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'handoff_logs';

    IF parent_kind IS NULL THEN
        CREATE TABLE public.handoff_logs (
            id integer NOT NULL DEFAULT nextval('public.handoff_logs_id_seq'::regclass),
            session_id varchar(64) NOT NULL,
            tenant_id varchar(64) NOT NULL,
            trigger_reason varchar(64) NOT NULL,
            tokens_at_handoff integer NOT NULL,
            context_window integer,
            handoff_prompt text,
            new_session_id varchar(64),
            created_at timestamp without time zone DEFAULT now(),
            summary_text text,
            summary_engine varchar(32),
            trigger_mode varchar(32),
            tokens_in_session integer,
            messages_in_session integer,
            skill_name varchar(64),
            duration_ms integer
        ) PARTITION BY RANGE (created_at);
    ELSIF parent_kind <> 'p' THEN
        RAISE EXCEPTION 'public.handoff_logs is not a partitioned parent after legacy conversion, relkind=%', parent_kind;
    END IF;
END
$do$;

ALTER TABLE public.handoff_logs
    ALTER COLUMN id SET DEFAULT nextval('public.handoff_logs_id_seq'::regclass);

DO $do$
DECLARE
    next_value bigint := 1;
BEGIN
    SELECT GREATEST(next_value, COALESCE(max(id), 0)) INTO next_value FROM public.handoff_logs_hot;
    SELECT GREATEST(next_value, COALESCE(max(id), 0)) INTO next_value FROM public.handoff_logs;
    IF to_regclass('public.handoff_logs_legacy_532') IS NOT NULL THEN
        EXECUTE 'SELECT GREATEST($1, COALESCE(max(id), 0)) FROM public.handoff_logs_legacy_532'
            INTO next_value USING next_value;
    END IF;
    PERFORM setval('public.handoff_logs_id_seq', next_value, true);
END
$do$;

CREATE TABLE IF NOT EXISTS public.handoff_logs_default
    PARTITION OF public.handoff_logs DEFAULT USING columnar;

CREATE OR REPLACE FUNCTION public.ensure_handoff_logs_partition(p_month timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    m0 timestamp := date_trunc('month', p_month AT TIME ZONE 'Asia/Shanghai');
    m1 timestamp := m0 + interval '1 month';
    part_name text := 'handoff_logs_' || to_char(p_month AT TIME ZONE 'Asia/Shanghai', 'YYYY_MM');
    existing_parent oid;
    existing_am text;
BEGIN
    SELECT i.inhparent, am.amname INTO existing_parent, existing_am
    FROM pg_inherits i
    JOIN pg_class child ON child.oid = i.inhrelid
    JOIN pg_namespace cn ON cn.oid = child.relnamespace
    LEFT JOIN pg_am am ON am.oid = child.relam
    WHERE cn.nspname = 'public' AND child.relname = part_name;

    IF existing_parent IS NOT NULL THEN
        IF existing_parent <> 'public.handoff_logs'::regclass OR existing_am <> 'columnar' THEN
            RAISE EXCEPTION 'public.% exists but is not an attached columnar handoff partition', part_name;
        END IF;
        RETURN;
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L) USING columnar',
        part_name, m0, m1
    );
    EXECUTE format('CREATE INDEX %I ON public.%I (created_at, id)', part_name || '_created_at_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)', part_name || '_session_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)', part_name || '_tenant_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', part_name || '_trigger_idx', part_name);
END;
$function$;

SELECT public.ensure_handoff_logs_partition(date_trunc('month', now()));
SELECT public.ensure_handoff_logs_partition(date_trunc('month', now()) + interval '1 month');

DO $view$
BEGIN
    IF to_regclass('public.handoff_logs_legacy_532') IS NOT NULL THEN
        EXECUTE $sql$
            CREATE OR REPLACE VIEW public.handoff_logs_with_current_month
            WITH (security_invoker=true) AS
            SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
                   context_window, handoff_prompt, new_session_id, created_at,
                   summary_text, summary_engine, trigger_mode, tokens_in_session,
                   messages_in_session, skill_name, duration_ms
            FROM public.handoff_logs
            UNION ALL
            SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
                   context_window, handoff_prompt, new_session_id, created_at,
                   summary_text, summary_engine, trigger_mode, tokens_in_session,
                   messages_in_session, skill_name, duration_ms
            FROM public.handoff_logs_hot
            UNION ALL
            SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
                   context_window, handoff_prompt, new_session_id, created_at,
                   summary_text, summary_engine, trigger_mode, tokens_in_session,
                   messages_in_session, skill_name, duration_ms
            FROM public.handoff_logs_legacy_532
        $sql$;
    ELSE
        EXECUTE $sql$
            CREATE OR REPLACE VIEW public.handoff_logs_with_current_month
            WITH (security_invoker=true) AS
            SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
                   context_window, handoff_prompt, new_session_id, created_at,
                   summary_text, summary_engine, trigger_mode, tokens_in_session,
                   messages_in_session, skill_name, duration_ms
            FROM public.handoff_logs
            UNION ALL
            SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
                   context_window, handoff_prompt, new_session_id, created_at,
                   summary_text, summary_engine, trigger_mode, tokens_in_session,
                   messages_in_session, skill_name, duration_ms
            FROM public.handoff_logs_hot
        $sql$;
    END IF;
END
$view$;

CREATE OR REPLACE FUNCTION public.promote_handoff_logs_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size integer DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    moved bigint := 0;
    month_value timestamptz;
BEGIN
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended('llm-gateway:promote:handoff_logs_hot', 0));

    CREATE TEMP TABLE _handoff_logs_promotion_batch ON COMMIT DROP AS
    SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
           context_window, handoff_prompt, new_session_id, created_at,
           summary_text, summary_engine, trigger_mode, tokens_in_session,
           messages_in_session, skill_name, duration_ms
    FROM public.handoff_logs_hot
    WHERE created_at < statement_timestamp() - p_retention
    ORDER BY created_at, id
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _handoff_logs_promotion_batch) THEN
        RETURN 0;
    END IF;

    FOR month_value IN
        SELECT DISTINCT date_trunc('month', created_at)::timestamptz
        FROM _handoff_logs_promotion_batch
    LOOP
        PERFORM public.ensure_handoff_logs_partition(month_value);
    END LOOP;

    -- DELETE and INSERT are one statement: a columnar insert failure rolls the
    -- statement and surrounding transaction back, preserving hot rows.
    WITH moved_rows AS (
        DELETE FROM public.handoff_logs_hot h
        USING _handoff_logs_promotion_batch b
        WHERE h.id = b.id
        RETURNING h.id, h.session_id, h.tenant_id, h.trigger_reason,
                  h.tokens_at_handoff, h.context_window, h.handoff_prompt,
                  h.new_session_id, h.created_at, h.summary_text,
                  h.summary_engine, h.trigger_mode, h.tokens_in_session,
                  h.messages_in_session, h.skill_name, h.duration_ms
    ), inserted_rows AS (
        INSERT INTO public.handoff_logs (
            id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
            context_window, handoff_prompt, new_session_id, created_at,
            summary_text, summary_engine, trigger_mode, tokens_in_session,
            messages_in_session, skill_name, duration_ms
        )
        SELECT id, session_id, tenant_id, trigger_reason, tokens_at_handoff,
               context_window, handoff_prompt, new_session_id, created_at,
               summary_text, summary_engine, trigger_mode, tokens_in_session,
               messages_in_session, skill_name, duration_ms
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO moved FROM inserted_rows;

    RETURN moved;
END;
$function$;

DO $do$
DECLARE
    constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT con.conname
        FROM pg_constraint con
        WHERE con.conrelid = to_regclass('public.handoff_pending_confirmations')
          AND con.confrelid IN (
              to_regclass('public.handoff_logs'),
              to_regclass('public.handoff_logs_legacy_532')
          )
          AND con.contype = 'f'
    LOOP
        EXECUTE format('ALTER TABLE public.handoff_pending_confirmations DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$do$;

INSERT INTO public.settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
VALUES ('lifecycle.handoff_logs_hot_retention_hours', '8', 'int', 'platform', 'lifecycle', now(), 'migration-532')
ON CONFLICT (key) DO NOTHING;

DO $do$
BEGIN
    IF to_regclass('public.handoff_logs') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'public.handoff_logs'::regclass)
       OR to_regclass('public.handoff_logs_hot') IS NULL
       OR to_regclass('public.handoff_logs_with_current_month') IS NULL
       OR to_regprocedure('public.ensure_handoff_logs_partition(timestamptz)') IS NULL
       OR to_regprocedure('public.promote_handoff_logs_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'handoff hot+columnar post-condition failed';
    END IF;
END
$do$;

COMMIT;
