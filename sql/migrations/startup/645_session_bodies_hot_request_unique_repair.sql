-- Migration 645: repair the missing session_bodies_hot request-id unique
-- constraint on 252 (172.16.2.210:5432/llm_gateway).
--
-- Symptom (production log, 2026-09-04):
--   "insert bodies: no unique or exclusion constraint matching the ON
--    CONFLICT specification"
--
-- Root cause: domains/session/v2/bodies_writer.go (WriteBodiesInTx, the
-- statement behind the "insert bodies" error) upserts
-- public.session_bodies_hot with
--   ON CONFLICT (tenant_id, request_id, partition_date)
-- whose arbiter is the constraint migration 614 defines as
--   session_bodies_with_current_month
--   UNIQUE (tenant_id, request_id, partition_date)
-- (sql/migrations/startup/614_session_bodies_hot.sql, "Add canonical unique
-- constraint required by the Session V2 migration contract").
--
-- Read-only audit against 252 (2026-09-04) shows the table exists with only
-- session_bodies_hot_pkey and session_bodies_hot_unique; the
-- session_bodies_with_current_month constraint never landed even though
-- schema_migrations records 614 as applied (2026-08-29 16:48:05+08). The
-- 252<->local sync pipeline only backfills columns/tables/indexes and never
-- ADDs UNIQUE constraints onto existing tables, and 614 itself is
-- checksum-frozen in repository_schema_migrations, so the repair must ship
-- as a fresh additive migration instead of editing 614.
--
-- Every other table on the mirror write path was verified intact on 252
-- (session_turns_hot_tenant_request_key, sessions_session_id_partition_date_key,
-- session_bodies_tenant_request_partition_key,
-- session_aggregate_outbox_unique_request, session_dim_pkey); only this one
-- constraint is missing.
--
-- Behavior:
--   - table missing            -> NOTICE + skip (614 owns creation; on a
--                                  fresh install 614 creates the constraint
--                                  inline and 645 becomes a no-op),
--   - constraint present       -> NOTICE + skip (idempotent),
--   - equivalent unique index  -> NOTICE + skip (ON CONFLICT inference only
--     on exactly those 3 keys     needs a matching unique index, whatever
--                                  its name),
--   - duplicate keys present   -> EXCEPTION, fail closed: the hot table is
--                                  an 8-hour transient window whose promote
--                                  worker (615/626) moves rows out, so
--                                  duplicates are pathological; resolve
--                                  explicitly instead of auto-deleting body
--                                  payloads inside a migration,
--   - otherwise                -> ALTER TABLE ... ADD CONSTRAINT.
--
-- Post-condition: either the named constraint or an equivalent unique index
-- exists, otherwise the migration raises.
--
-- Down: drops the constraint (fresh installs fall back to 614's own
-- definition of the same name; every environment falls back to the broken
-- pre-645 state, which is exactly what a rollback means here).
BEGIN;

DO $$
DECLARE
    v_dup   BIGINT;
    v_equiv TEXT;
    v_keys  smallint[];
BEGIN
    IF to_regclass('public.session_bodies_hot') IS NULL THEN
        RAISE NOTICE 'migration 645: public.session_bodies_hot does not exist yet; 614 owns creation, nothing to repair';
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.session_bodies_hot'::regclass
          AND conname = 'session_bodies_with_current_month'
          AND contype = 'u'
          AND convalidated
    ) THEN
        RAISE NOTICE 'migration 645: session_bodies_with_current_month already present; nothing to do';
    ELSE
        -- ON CONFLICT (tenant_id, request_id, partition_date) can also be
        -- satisfied by a plain (non-partial, non-expression) unique index on
        -- exactly those three key columns, regardless of its name.
        SELECT array_agg(a.attnum ORDER BY a.attnum)
          INTO v_keys
          FROM pg_attribute a
         WHERE a.attrelid = 'public.session_bodies_hot'::regclass
           AND a.attname IN ('tenant_id', 'request_id', 'partition_date');

        SELECT i.relname
          INTO v_equiv
          FROM pg_index x
          JOIN pg_class i ON i.oid = x.indexrelid
         WHERE x.indrelid = 'public.session_bodies_hot'::regclass
           AND x.indisunique
           AND x.indisvalid
           AND x.indpred IS NULL      -- partial indexes are not arbiters for a bare column list
           AND x.indexprs IS NULL     -- expression indexes are not inferable either
           AND x.indnkeyatts = 3
           AND (SELECT array_agg(u.k ORDER BY u.k)
                  FROM unnest(x.indkey::int2[]) AS u(k)
                 WHERE u.k <> 0) = v_keys
         LIMIT 1;

        IF v_equiv IS NOT NULL THEN
            RAISE NOTICE 'migration 645: equivalent unique index % already satisfies ON CONFLICT (tenant_id, request_id, partition_date); skipping constraint creation', v_equiv;
        ELSE
            SELECT count(*)
              INTO v_dup
              FROM (
                  SELECT 1
                  FROM public.session_bodies_hot
                  GROUP BY tenant_id, request_id, partition_date
                  HAVING count(*) > 1
              ) d;

            IF v_dup > 0 THEN
                RAISE EXCEPTION 'migration 645 blocked: % duplicate (tenant_id, request_id, partition_date) group(s) in session_bodies_hot; keep one row per key (latest ts wins) or let promote_session_bodies_hot_to_partition drain the hot window, then re-run', v_dup;
            END IF;

            EXECUTE 'ALTER TABLE public.session_bodies_hot
                     ADD CONSTRAINT session_bodies_with_current_month
                     UNIQUE (tenant_id, request_id, partition_date)';
            RAISE NOTICE 'migration 645: added session_bodies_with_current_month UNIQUE (tenant_id, request_id, partition_date)';
        END IF;
    END IF;

    -- Post-condition: the arbiter for bodies_writer.go must be satisfiable.
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.session_bodies_hot'::regclass
          AND contype = 'u'
          AND convalidated
          AND pg_get_constraintdef(oid) = 'UNIQUE (tenant_id, request_id, partition_date)'
    ) AND v_equiv IS NULL THEN
        RAISE EXCEPTION 'migration 645 failed: no valid arbiter for ON CONFLICT (tenant_id, request_id, partition_date) on public.session_bodies_hot after repair';
    END IF;
END
$$;

COMMIT;
