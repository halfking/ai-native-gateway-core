-- 确认分区为空后重建为 heap（columnar 不支持写入端必需的 ON CONFLICT）
DO $$
DECLARE
  cnt bigint;
BEGIN
  SELECT count(*) INTO cnt FROM public.session_bodies_2026_08;
  IF cnt > 0 THEN
    RAISE EXCEPTION 'session_bodies_2026_08 not empty: %', cnt;
  END IF;
  SELECT count(*) INTO cnt FROM public.session_bodies_2026_07;
  IF cnt > 0 THEN
    RAISE EXCEPTION 'session_bodies_2026_07 not empty: %', cnt;
  END IF;

  DROP TABLE public.session_bodies_2026_07;
  DROP TABLE public.session_bodies_2026_08;
  CREATE TABLE public.session_bodies_2026_07 PARTITION OF public.session_bodies
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
  CREATE TABLE public.session_bodies_2026_08 PARTITION OF public.session_bodies
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
END $$;
SELECT c.relname||' am='||(select amname from pg_am where oid=c.relam)
from pg_class c join pg_namespace n on n.oid=c.relnamespace
where n.nspname='public' and c.relname like 'session_bodies_2026%' and c.relkind='r' order by 1;
