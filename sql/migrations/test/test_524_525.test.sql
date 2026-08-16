-- Migrations 524/525 verification: scoped dimensions and session_turns hot path.
-- Usage: psql <dsn> -v ON_ERROR_STOP=1 -f sql/migrations/test/test_524_525.test.sql

\set ON_ERROR_STOP on

DO $$
DECLARE
    v_column TEXT;
    v_partition REGCLASS;
    v_index RECORD;
    v_parent_columns TEXT[];
    v_hot_columns TEXT[];
    v_view_columns TEXT[];
    v_moved BIGINT;
    v_hot_id BIGINT;
    v_followup_hot_id BIGINT;
    v_rejected BOOLEAN;
BEGIN
    IF current_setting('server_version_num')::integer < 150000 THEN
        RAISE EXCEPTION '524/525 tests require PostgreSQL 15 or newer';
    END IF;

    FOREACH v_column IN ARRAY ARRAY[
        'project_id', 'namespace', 'parent_request_id', 'task_type'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns'::regclass
              AND attname = v_column
              AND atttypid = 'text'::regtype
              AND attnum > 0
              AND NOT attisdropped
              AND NOT attnotnull
        ) THEN
            RAISE EXCEPTION '524: parent column % is not nullable TEXT', v_column;
        END IF;

        FOR v_partition IN
            SELECT relid
            FROM pg_partition_tree('public.session_turns'::regclass)
            WHERE isleaf
        LOOP
            IF NOT EXISTS (
                SELECT 1
                FROM pg_attribute
                WHERE attrelid = v_partition
                  AND attname = v_column
                  AND atttypid = 'text'::regtype
                  AND attnum > 0
                  AND NOT attisdropped
                  AND NOT attnotnull
            ) THEN
                RAISE EXCEPTION '524: %.% was not propagated', v_partition, v_column;
            END IF;
        END LOOP;
    END LOOP;

    FOR v_index IN
        SELECT *
        FROM (VALUES
            ('idx_session_turns_tenant_project', 'project_id'),
            ('idx_session_turns_tenant_namespace', 'namespace'),
            ('idx_session_turns_tenant_parent_request', 'parent_request_id'),
            ('idx_session_turns_tenant_task_type', 'task_type')
        ) expected(index_name, scoped_column)
    LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM pg_class i
            JOIN pg_index x ON x.indexrelid = i.oid
            JOIN pg_attribute first_key
              ON first_key.attrelid = x.indrelid
             AND first_key.attnum = (x.indkey::smallint[])[0]
            WHERE i.relnamespace = 'public'::regnamespace
              AND i.relname = v_index.index_name
              AND i.relkind = 'I'
              AND x.indrelid = 'public.session_turns'::regclass
              AND first_key.attname = 'tenant_id'
              AND pg_get_expr(x.indpred, x.indrelid) =
                  format('(%I IS NOT NULL)', v_index.scoped_column)
        ) THEN
            RAISE EXCEPTION '524: parent partial index % is missing or malformed',
                v_index.index_name;
        END IF;
    END LOOP;

    IF to_regclass('public.session_turns_hot') IS NULL THEN
        RAISE EXCEPTION '525: public.session_turns_hot is missing';
    END IF;

    SELECT array_agg(attname ORDER BY attnum)
    INTO v_parent_columns
    FROM pg_attribute
    WHERE attrelid = 'public.session_turns'::regclass
      AND attnum > 0 AND NOT attisdropped;

    SELECT array_agg(attname ORDER BY attnum)
    INTO v_hot_columns
    FROM pg_attribute
    WHERE attrelid = 'public.session_turns_hot'::regclass
      AND attnum > 0 AND NOT attisdropped;

    SELECT array_agg(column_name ORDER BY ordinal_position)
    INTO v_view_columns
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'session_turns_with_current_month';

    IF cardinality(v_parent_columns) <> 50 OR cardinality(v_hot_columns) <> 50 THEN
        RAISE EXCEPTION '525: expected 50 parent/hot columns, found parent=% hot=%',
            cardinality(v_parent_columns), cardinality(v_hot_columns);
    END IF;

    IF EXISTS (
        WITH parent_contract AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns'::regclass
              AND attnum > 0 AND NOT attisdropped
        ), hot_contract AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns_hot'::regclass
              AND attnum > 0 AND NOT attisdropped
        )
        SELECT 1
        FROM parent_contract p
        FULL JOIN hot_contract h USING (attname)
        WHERE p.attname IS NULL OR h.attname IS NULL
           OR p.atttypid <> h.atttypid
           OR p.atttypmod <> h.atttypmod
           OR p.attnotnull <> h.attnotnull
    ) THEN
        RAISE EXCEPTION '525: hot column contract differs from parent';
    END IF;

    IF v_view_columns IS DISTINCT FROM v_hot_columns THEN
        RAISE EXCEPTION '525: view column order differs from explicit hot order';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.session_turns'::regclass
          AND conname = 'session_turns_submit_mode_check'
          AND convalidated
          AND pg_get_constraintdef(oid) ~ 'attachment_only'
    ) THEN
        RAISE EXCEPTION '525: parent submit_mode constraint does not allow attachment_only';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_attrdef d
        JOIN pg_attribute a
          ON a.attrelid = d.adrelid AND a.attnum = d.adnum
        WHERE d.adrelid = 'public.session_turns_hot'::regclass
          AND a.attname = 'id'
          AND pg_get_expr(d.adbin, d.adrelid)
              LIKE 'nextval(%session_turns_id_seq%'
    ) THEN
        RAISE EXCEPTION '525: hot id must reuse public.session_turns_id_seq';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_class
        WHERE oid = 'public.session_turns_hot'::regclass
          AND relrowsecurity
    ) THEN
        RAISE EXCEPTION '525: hot RLS is not enabled';
    END IF;

    IF (SELECT count(*) FROM pg_policies
        WHERE schemaname = 'public' AND tablename = 'session_turns_hot') <> 3 THEN
        RAISE EXCEPTION '525: expected three hot RLS policies';
    END IF;

    IF to_regprocedure('public.session_turns_advisory_lock_key(text,text)') IS NULL THEN
        RAISE EXCEPTION '525: shared session advisory lock key function is missing';
    END IF;

    IF pg_get_functiondef(
        'public.promote_session_turns_hot_to_partition(interval,integer)'::regprocedure
    ) !~ 'session_turns_advisory_lock_key' THEN
        RAISE EXCEPTION '525: promotion does not use the shared session advisory lock';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE oid = 'public.session_turns_with_current_month'::regclass
          AND 'security_invoker=true' = ANY (reloptions)
    ) THEN
        RAISE EXCEPTION '525: view must use security_invoker=true';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'session_turns_hot'
          AND policyname = 'session_turns_hot_owner_filter'
          AND permissive = 'RESTRICTIVE'
          AND qual::TEXT ~ 'request_logs_hot'
          AND qual::TEXT ~ 'request_logs([^_[:alnum:]]|$)'
    ) THEN
        RAISE EXCEPTION '525: owner filter must use hot and historical request sources';
    END IF;

    v_rejected := FALSE;
    BEGIN
        PERFORM public.promote_session_turns_hot_to_partition(INTERVAL '0 seconds', 1);
    EXCEPTION WHEN OTHERS THEN
        v_rejected := TRUE;
    END;
    IF NOT v_rejected THEN
        RAISE EXCEPTION '525: zero retention was accepted';
    END IF;

    v_rejected := FALSE;
    BEGIN
        PERFORM public.promote_session_turns_hot_to_partition(INTERVAL '1 day', 0);
    EXCEPTION WHEN OTHERS THEN
        v_rejected := TRUE;
    END;
    IF NOT v_rejected THEN
        RAISE EXCEPTION '525: zero batch size was accepted';
    END IF;

    DELETE FROM public.session_turns_hot
    WHERE tenant_id = 'test-525-tenant'
      AND request_id LIKE 'test-525-%';
    DELETE FROM public.session_turns
    WHERE tenant_id = 'test-525-tenant'
      AND request_id LIKE 'test-525-%';

    INSERT INTO public.session_turns_hot (
        id, session_id, turn_no, tenant_id, request_id,
        project_id, namespace, parent_request_id, task_type,
        ts, partition_date, submit_mode, source_kind, quality
    ) VALUES (
        -9223372036854770523,
        'test-525-session-ok', 1, 'test-525-tenant', 'test-525-promote-ok',
        'project-525', 'namespace-525', 'parent-525', 'analysis',
        '-infinity'::TIMESTAMPTZ, CURRENT_DATE,
        'attachment_only', 'live', 'verified'
    ) RETURNING id INTO v_hot_id;

    SELECT public.promote_session_turns_hot_to_partition(INTERVAL '7 days', 1)
    INTO v_moved;

    IF v_moved <> 1 THEN
        RAISE EXCEPTION '525: expected one promoted row, got %', v_moved;
    END IF;

    IF EXISTS (
        SELECT 1 FROM public.session_turns_hot
        WHERE id = v_hot_id AND partition_date = CURRENT_DATE
    ) THEN
        RAISE EXCEPTION '525: promoted row remains in hot table';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM public.session_turns
        WHERE id = v_hot_id
          AND partition_date = CURRENT_DATE
          AND project_id = 'project-525'
          AND namespace = 'namespace-525'
          AND parent_request_id = 'parent-525'
          AND task_type = 'analysis'
          AND submit_mode = 'attachment_only'
    ) THEN
        RAISE EXCEPTION '525: promoted row or scoped values are missing';
    END IF;

    INSERT INTO public.session_turns (
        session_id, turn_no, tenant_id, request_id,
        ts, partition_date, submit_mode, source_kind, quality
    ) VALUES (
        'test-525-session-conflict-parent', 2,
        'test-525-tenant', 'test-525-promote-conflict',
        '-infinity'::TIMESTAMPTZ, CURRENT_DATE,
        'full', 'live', 'verified'
    );

    INSERT INTO public.session_turns_hot (
        id, session_id, turn_no, tenant_id, request_id,
        ts, partition_date, submit_mode, source_kind, quality
    ) VALUES (
        -9223372036854770524,
        'test-525-session-conflict-hot', 3,
        'test-525-tenant', 'test-525-promote-conflict',
        '-infinity'::TIMESTAMPTZ, CURRENT_DATE,
        'full', 'live', 'verified'
    ) RETURNING id INTO v_hot_id;

    INSERT INTO public.session_turns_hot (
        id, session_id, turn_no, tenant_id, request_id,
        ts, partition_date, submit_mode, source_kind, quality
    ) VALUES (
        -9223372036854770525,
        'test-525-session-followup', 4,
        'test-525-tenant', 'test-525-promote-followup',
        '-infinity'::TIMESTAMPTZ, CURRENT_DATE,
        'full', 'live', 'verified'
    ) RETURNING id INTO v_followup_hot_id;

    SELECT public.promote_session_turns_hot_to_partition(INTERVAL '7 days', 1)
    INTO v_moved;

    IF v_moved <> 1 THEN
        RAISE EXCEPTION '525: duplicate poison row blocked follow-up promote';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM public.session_turns_hot
        WHERE id = v_hot_id AND partition_date = CURRENT_DATE
    ) THEN
        RAISE EXCEPTION '525: duplicate hot row must remain for explicit reconciliation';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.session_turns_hot
        WHERE id = v_followup_hot_id AND partition_date = CURRENT_DATE
    ) OR NOT EXISTS (
        SELECT 1 FROM public.session_turns
        WHERE id = v_followup_hot_id AND partition_date = CURRENT_DATE
    ) THEN
        RAISE EXCEPTION '525: normal follow-up row was not promoted past duplicate';
    END IF;
    IF (
        SELECT count(*)
        FROM public.session_turns_with_current_month
        WHERE tenant_id = 'test-525-tenant'
          AND request_id = 'test-525-promote-conflict'
    ) <> 1 THEN
        RAISE EXCEPTION '525: unified view exposed both hot and archived duplicates';
    END IF;

    DELETE FROM public.session_turns_hot
    WHERE tenant_id = 'test-525-tenant'
      AND request_id LIKE 'test-525-%';
    DELETE FROM public.session_turns
    WHERE tenant_id = 'test-525-tenant'
      AND request_id LIKE 'test-525-%';
END $$;

-- Exercise the security-invoker view as a real NOBYPASSRLS non-owner role.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'session_turns_rls_test') THEN
        CREATE ROLE session_turns_rls_test NOLOGIN NOBYPASSRLS;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO session_turns_rls_test;
GRANT SELECT ON public.session_turns, public.session_turns_hot,
    public.session_turns_with_current_month,
    public.request_logs, public.request_logs_hot
TO session_turns_rls_test;

INSERT INTO public.request_logs_hot (gw_session_id, owner_user, ts)
VALUES ('test-525-rls-parent', 'owner-525', NOW() - INTERVAL '2 hours');
INSERT INTO public.request_logs (gw_session_id, owner_user, ts)
VALUES ('test-525-rls-hot', 'owner-525', NOW() - INTERVAL '1 hour');
INSERT INTO public.session_turns (
    session_id, turn_no, tenant_id, request_id, ts, partition_date
) VALUES (
    'test-525-rls-parent', 1, 'test-525-rls-tenant',
    'test-525-rls-parent-request', NOW(), CURRENT_DATE
);
INSERT INTO public.session_turns_hot (
    session_id, turn_no, tenant_id, request_id, ts, partition_date
) VALUES (
    'test-525-rls-hot', 1, 'test-525-rls-tenant',
    'test-525-rls-hot-request', NOW(), CURRENT_DATE
);

SET ROLE session_turns_rls_test;
SELECT set_config('app.current_tenant', 'test-525-rls-tenant', false);
SELECT set_config('app.current_role', 'tenant_admin', false);
SELECT set_config('app.bypass_rls', 'false', false);
SELECT set_config('app.current_user', 'owner-525', false);
DO $$
BEGIN
    IF (SELECT count(*) FROM public.session_turns_with_current_month
        WHERE tenant_id = 'test-525-rls-tenant') <> 2 THEN
        RAISE EXCEPTION '525: owner cannot see both hot and parent rows through security-invoker view';
    END IF;
END $$;

SELECT set_config('app.current_user', 'stranger-525', false);
DO $$
BEGIN
    IF (SELECT count(*) FROM public.session_turns_with_current_month
        WHERE tenant_id = 'test-525-rls-tenant') <> 0 THEN
        RAISE EXCEPTION '525: non-owner can see rows through security-invoker view';
    END IF;
END $$;

SELECT set_config('app.current_role', 'super_admin', false);
DO $$
BEGIN
    IF (SELECT count(*) FROM public.session_turns_with_current_month
        WHERE tenant_id = 'test-525-rls-tenant') <> 2 THEN
        RAISE EXCEPTION '525: super_admin cannot see both hot and parent rows';
    END IF;
END $$;
RESET ROLE;

DELETE FROM public.session_turns_hot
WHERE tenant_id = 'test-525-rls-tenant';
DELETE FROM public.session_turns
WHERE tenant_id = 'test-525-rls-tenant';
DELETE FROM public.request_logs_hot
WHERE gw_session_id IN ('test-525-rls-parent', 'test-525-rls-hot');
DELETE FROM public.request_logs
WHERE gw_session_id IN ('test-525-rls-parent', 'test-525-rls-hot');
REVOKE ALL ON public.session_turns, public.session_turns_hot,
    public.session_turns_with_current_month,
    public.request_logs, public.request_logs_hot
FROM session_turns_rls_test;
REVOKE USAGE ON SCHEMA public FROM session_turns_rls_test;
DROP ROLE session_turns_rls_test;

SELECT '524/525: all checks passed' AS result;
