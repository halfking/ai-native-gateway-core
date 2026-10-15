-- Down migration for 860: revert the pending-delete rename.
--
-- ⚠ Valid ONLY while the `_`-prefixed family still exists (i.e. before the
-- actual DROP). Once the family is dropped there is nothing to revert to.
-- Function bodies are rewritten back with the same boundary rules as the up
-- migration (function names never contained the guard-sensitive forms).

BEGIN;

\set ON_ERROR_STOP on

DO $rename$
DECLARE
  old_name text;
  new_name text;
  pairs constant text[] := ARRAY[
    '_request_logs_bodies_hot',
    '_request_logs_bodies',
    '_request_logs_hot',
    '_request_logs',
    '_request_logs_archive'
  ];
BEGIN
  FOREACH new_name IN ARRAY pairs LOOP
    old_name := substring(new_name from 2);
    IF to_regclass('public.' || old_name) IS NOT NULL THEN
      IF to_regclass('public.' || new_name) IS NOT NULL THEN
        RAISE EXCEPTION 'down 860: both % and public.% exist — resolve manually', old_name, new_name;
      END IF;
      RAISE NOTICE 'down 860: public.% already reverted (skip)', new_name;
      CONTINUE;
    END IF;
    IF to_regclass('public.' || new_name) IS NULL THEN
      RAISE NOTICE 'down 860: public.% not found (skip)', new_name;
      CONTINUE;
    END IF;
    EXECUTE format('ALTER TABLE public.%I RENAME TO %I', new_name, old_name);
  END LOOP;

  IF to_regclass('public._request_logs_id_seq') IS NOT NULL
     AND to_regclass('public.request_logs_id_seq') IS NULL THEN
    EXECUTE 'ALTER SEQUENCE public._request_logs_id_seq RENAME TO request_logs_id_seq';
  END IF;
END
$rename$;

DO $rewrite$
DECLARE
  r record;
  def text;
  tokens constant text[] := ARRAY[
    '_request_logs_bodies_hot',
    '_request_logs_bodies',
    '_request_logs_hot',
    '_request_logs_archive',
    '_request_logs'
  ];
  tk text;
BEGIN
  FOR r IN
    SELECT p.oid AS fnoid, pg_get_functiondef(p.oid) AS def
      FROM pg_proc p
      JOIN pg_namespace nsp ON nsp.oid = p.pronamespace
     WHERE nsp.nspname = 'public'
       AND p.prosrc ~ '(?<![a-zA-Z0-9_])(_request_logs(_bodies_hot|_bodies|_hot|_archive)?)(?![a-zA-Z0-9_])'
  LOOP
    def := r.def;
    FOREACH tk IN ARRAY tokens LOOP
      def := regexp_replace(def, '(?<![a-zA-Z0-9_])' || tk || '(?![a-zA-Z0-9_])', substring(tk from 2), 'g');
    END LOOP;
    EXECUTE def;
  END LOOP;
END
$rewrite$;

COMMIT;
