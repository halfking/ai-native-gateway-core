-- Migration 533: make request_wal_bodies compatible with its request_id upsert.
--
-- request_logger persists outbound request bodies with ON CONFLICT (request_id).
-- Older schema snapshots created this table without a unique request_id arbiter,
-- so this migration repairs drift by retaining the newest body per request_id.
\set ON_ERROR_STOP on

BEGIN;

LOCK TABLE public.request_wal_bodies IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    duplicate_rows bigint;
    has_unique_arbiter boolean;
BEGIN
    IF to_regclass('public.request_wal_bodies') IS NULL THEN
        RAISE EXCEPTION '533: public.request_wal_bodies is missing; apply the request WAL baseline first';
    END IF;

    DELETE FROM public.request_wal_bodies body
    WHERE body.ctid IN (
        SELECT ctid
        FROM (
            SELECT ctid,
                   row_number() OVER (
                       PARTITION BY request_id
                       ORDER BY created_at DESC, ctid DESC
                   ) AS row_number
            FROM public.request_wal_bodies
        ) AS ranked
        WHERE row_number > 1
    );
    GET DIAGNOSTICS duplicate_rows = ROW_COUNT;

    IF duplicate_rows > 0 THEN
        RAISE NOTICE '533: removed % duplicate request_wal_bodies rows, retaining newest body per request_id', duplicate_rows;
    END IF;

    SELECT EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_attribute a
          ON a.attrelid = i.indrelid
         AND a.attnum = i.indkey[0]
        WHERE i.indrelid = 'public.request_wal_bodies'::regclass
          AND i.indisvalid
          AND i.indisunique
          AND i.indpred IS NULL
          AND i.indnkeyatts = 1
          AND a.attname = 'request_id'
    )
      INTO has_unique_arbiter;

    IF NOT has_unique_arbiter THEN
        ALTER TABLE public.request_wal_bodies
            ADD CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id);
    END IF;
END $$;

COMMIT;
