-- 865: reconcile the physical body-writer contract independently of 708's ledger.
-- 708 must not be replayed to repair drift: it also drops legacy sessions columns
-- and updates historical partitions. This additive repair preserves every row.
BEGIN;

ALTER TABLE public.session_bodies
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'turn_delta';
ALTER TABLE public.session_bodies_hot
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'turn_delta';
-- Existing nullable columns with NULL rows require explicit data reconciliation;
-- refuse instead of silently rewriting historical/columnar bodies.
ALTER TABLE public.session_bodies ALTER COLUMN kind SET DEFAULT 'turn_delta',
    ALTER COLUMN kind SET NOT NULL;
ALTER TABLE public.session_bodies_hot ALTER COLUMN kind SET DEFAULT 'turn_delta',
    ALTER COLUMN kind SET NOT NULL;

CREATE OR REPLACE VIEW public.session_bodies_unified AS
SELECT id, session_id, turn_no, tenant_id, request_id, request_delta,
       response_delta, outbound_body, request_attachments, response_attachments,
       ts, partition_date, kind
  FROM public.session_bodies_hot
UNION ALL
SELECT id, session_id, turn_no, tenant_id, request_id, request_delta,
       response_delta, outbound_body, request_attachments, response_attachments,
       ts, partition_date, kind
  FROM public.session_bodies;
ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_bodies_final_full
    ON public.session_bodies (tenant_id, session_id, partition_date)
    WHERE kind = 'final_full';
CREATE UNIQUE INDEX IF NOT EXISTS uq_session_bodies_hot_final_full
    ON public.session_bodies_hot (tenant_id, session_id, partition_date)
    WHERE kind = 'final_full';

-- Also called after the revision channel has checked all fingerprints. A
-- matching marker proves delivery, never the current physical schema.
CREATE OR REPLACE FUNCTION public.assert_session_bodies_kind_contract()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    relation_name text;
    index_name text;
BEGIN
    FOREACH relation_name IN ARRAY ARRAY['session_bodies', 'session_bodies_hot']
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_attribute a
            JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
            WHERE a.attrelid = to_regclass('public.' || relation_name)
              AND a.attname = 'kind' AND NOT a.attisdropped
              AND a.atttypid = 'text'::regtype AND a.attnotnull
              AND pg_get_expr(d.adbin, d.adrelid) = '''turn_delta''::text'
        ) THEN
            RAISE EXCEPTION 'session_bodies kind contract: %.kind must be text NOT NULL DEFAULT turn_delta', relation_name;
        END IF;
        index_name := CASE relation_name WHEN 'session_bodies'
            THEN 'uq_session_bodies_final_full' ELSE 'uq_session_bodies_hot_final_full' END;
        IF NOT EXISTS (
            SELECT 1 FROM pg_index i
            WHERE i.indexrelid = to_regclass('public.' || index_name)
              AND i.indrelid = to_regclass('public.' || relation_name)
              AND i.indisunique AND i.indisvalid AND i.indnkeyatts = 3 AND i.indnatts = 3
              AND regexp_replace(pg_get_expr(i.indpred, i.indrelid), '[[:space:]()]', '', 'g') = 'kind=''final_full''::text'
              AND (SELECT array_agg(a.attname::text ORDER BY k.position)
                     FROM unnest(i.indkey) WITH ORDINALITY k(attnum, position)
                     JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum)
                  = ARRAY['tenant_id', 'session_id', 'partition_date']
        ) THEN
            RAISE EXCEPTION 'session_bodies kind contract: % final_full unique index missing or incompatible', relation_name;
        END IF;
    END LOOP;
    IF (SELECT count(*) FROM pg_attribute
         WHERE attrelid = to_regclass('public.session_bodies_unified')
           AND attnum > 0 AND NOT attisdropped) <> 13
       OR NOT EXISTS (
           SELECT 1 FROM pg_attribute
            WHERE attrelid = to_regclass('public.session_bodies_unified')
              AND attnum = 13 AND attname = 'kind' AND atttypid = 'text'::regtype
              AND NOT attisdropped
       ) THEN
        RAISE EXCEPTION 'session_bodies kind contract: unified view must expose kind as its thirteenth column';
    END IF;
END;
$$;
SELECT public.assert_session_bodies_kind_contract();

INSERT INTO public.schema_migrations(version, description)
VALUES ('865', 'additive session_bodies kind physical contract reconciliation')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
