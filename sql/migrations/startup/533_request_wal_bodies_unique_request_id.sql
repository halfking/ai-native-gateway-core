-- Migration 533: make request_wal_bodies compatible with its request_id upsert.
--
-- request_logger persists outbound request bodies with ON CONFLICT (request_id).
-- Older schema snapshots created this table without a unique request_id arbiter,
-- so this migration repairs drift without discarding any existing body payloads.
\set ON_ERROR_STOP on

BEGIN;

LOCK TABLE public.request_wal_bodies IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    duplicate_groups bigint;
    has_unique_arbiter boolean;
BEGIN
    IF to_regclass('public.request_wal_bodies') IS NULL THEN
        RAISE EXCEPTION '533: public.request_wal_bodies is missing; apply the request WAL baseline first';
    END IF;

    SELECT count(*)
      INTO duplicate_groups
      FROM (
          SELECT request_id
          FROM public.request_wal_bodies
          GROUP BY request_id
          HAVING count(*) > 1
      ) AS duplicates;

    IF duplicate_groups > 0 THEN
        RAISE EXCEPTION '533: request_wal_bodies contains % duplicate request_id groups; reconcile body records before retrying', duplicate_groups;
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
