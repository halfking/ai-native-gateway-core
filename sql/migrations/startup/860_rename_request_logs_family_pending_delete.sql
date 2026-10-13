-- Migration 860 (2026-10-14): rename the request_logs family to `_`-prefixed
-- names as the pending-delete marker (S4 retirement).
--
-- What moves (physical relations only):
--   request_logs            -> _request_logs
--   request_logs_hot        -> _request_logs_hot
--   request_logs_bodies     -> _request_logs_bodies
--   request_logs_bodies_hot -> _request_logs_bodies_hot
--   request_logs_archive    -> _request_logs_archive
--   request_logs_id_seq     -> _request_logs_id_seq
--
-- What deliberately does NOT move:
--   * canonical view names (request_logs_with_current_month,
--     request_logs_bodies_with_current_month, request_logs_bodies_progress,
--     *_without_* variants) — they are the compatibility read layer and follow
--     the table renames by OID (verified: pg_get_viewdef shows the new names
--     after this migration, views keep working with zero DDL here);
--   * monthly partitions and archive children (request_logs_2026_10,
--     request_logs_archive_2026_08, request_logs_default, ...) — they inherit
--     the marker through their renamed parents and are dropped together with
--     them; their index names likewise stay;
--   * FUNCTION names — bodies are rewritten in place; names are the Go-side
--     scheduler contract (partition_manager fnName strings).
--
-- Safety properties:
--   * idempotent: replays no-op when the prefixed names already exist;
--   * loud: raises if BOTH old and new names exist (ambiguity nobody should
--     resolve silently), and asserts after the rewrite that no public function
--     body references an unprefixed family name;
--   * rollback: the .down migration reverses renames + function bodies. It is
--     valid only BEFORE the family is actually dropped.
--
-- Deploy note: between this migration and the gateway cutover the OLD binary
-- serves reads against renamed tables and will error briefly (blue-green
-- handover window). Writes are already gated off
-- (storage.request_logs_write_enabled=false), so no writes race the rename.

BEGIN;

\set ON_ERROR_STOP on

-- ── 1. Rename the family ──────────────────────────────────────────────────
-- One guarded block per relation, with the full ALTER statement spelled out
-- inside each EXECUTE literal. That static text is load-bearing for the
-- retirement test infrastructure: admin/request_logs_reader_population_test.go
-- replays sql/migrations textually (liveRelations) and parses
-- `ALTER TABLE <old> RENAME TO <new>` to learn that the old name is dead and
-- the prefixed name is live. A dynamic format('%I', ...) rename would be
-- invisible to that replay, and the population gates would go stale.
DO $rename_bodies_hot$
BEGIN
  IF to_regclass('public._request_logs_bodies_hot') IS NOT NULL THEN
    IF to_regclass('public.request_logs_bodies_hot') IS NOT NULL THEN
      RAISE EXCEPTION 'migration 860: both request_logs_bodies_hot and _request_logs_bodies_hot exist — resolve manually before replaying';
    END IF;
  ELSIF to_regclass('public.request_logs_bodies_hot') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.request_logs_bodies_hot RENAME TO _request_logs_bodies_hot';
    RAISE NOTICE 'migration 860: request_logs_bodies_hot -> _request_logs_bodies_hot';
  ELSE
    RAISE NOTICE 'migration 860: request_logs_bodies_hot not found (skip)';
  END IF;
END
$rename_bodies_hot$;

DO $rename_bodies$
BEGIN
  IF to_regclass('public._request_logs_bodies') IS NOT NULL THEN
    IF to_regclass('public.request_logs_bodies') IS NOT NULL THEN
      RAISE EXCEPTION 'migration 860: both request_logs_bodies and _request_logs_bodies exist — resolve manually before replaying';
    END IF;
  ELSIF to_regclass('public.request_logs_bodies') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.request_logs_bodies RENAME TO _request_logs_bodies';
    RAISE NOTICE 'migration 860: request_logs_bodies -> _request_logs_bodies';
  ELSE
    RAISE NOTICE 'migration 860: request_logs_bodies not found (skip)';
  END IF;
END
$rename_bodies$;

DO $rename_hot$
BEGIN
  IF to_regclass('public._request_logs_hot') IS NOT NULL THEN
    IF to_regclass('public.request_logs_hot') IS NOT NULL THEN
      RAISE EXCEPTION 'migration 860: both request_logs_hot and _request_logs_hot exist — resolve manually before replaying';
    END IF;
  ELSIF to_regclass('public.request_logs_hot') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot RENAME TO _request_logs_hot';
    RAISE NOTICE 'migration 860: request_logs_hot -> _request_logs_hot';
  ELSE
    RAISE NOTICE 'migration 860: request_logs_hot not found (skip)';
  END IF;
END
$rename_hot$;

DO $rename_main$
BEGIN
  IF to_regclass('public._request_logs') IS NOT NULL THEN
    IF to_regclass('public.request_logs') IS NOT NULL THEN
      RAISE EXCEPTION 'migration 860: both request_logs and _request_logs exist — resolve manually before replaying';
    END IF;
  ELSIF to_regclass('public.request_logs') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.request_logs RENAME TO _request_logs';
    RAISE NOTICE 'migration 860: request_logs -> _request_logs';
  ELSE
    RAISE NOTICE 'migration 860: request_logs not found (skip)';
  END IF;
END
$rename_main$;

DO $rename_archive$
BEGIN
  IF to_regclass('public._request_logs_archive') IS NOT NULL THEN
    IF to_regclass('public.request_logs_archive') IS NOT NULL THEN
      RAISE EXCEPTION 'migration 860: both request_logs_archive and _request_logs_archive exist — resolve manually before replaying';
    END IF;
  ELSIF to_regclass('public.request_logs_archive') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.request_logs_archive RENAME TO _request_logs_archive';
    RAISE NOTICE 'migration 860: request_logs_archive -> _request_logs_archive';
  ELSE
    RAISE NOTICE 'migration 860: request_logs_archive not found (skip)';
  END IF;
END
$rename_archive$;

-- Sequence: standalone (verified: pg_get_serial_sequence on the family is
-- empty, so no OWNED BY auto-rename happens). Column defaults store OIDs,
-- so a rename cannot break nextval() references.
DO $rename_seq$
BEGIN
  IF to_regclass('public._request_logs_id_seq') IS NULL
     AND to_regclass('public.request_logs_id_seq') IS NOT NULL THEN
    EXECUTE 'ALTER SEQUENCE public.request_logs_id_seq RENAME TO _request_logs_id_seq';
    RAISE NOTICE 'migration 860: request_logs_id_seq -> _request_logs_id_seq';
  END IF;
END
$rename_seq$;

-- ── 2. Rewrite plpgsql function bodies that reference the family ─────────
-- pg_get_functiondef round-trips every matching function with the five tokens
-- rewritten under word boundaries. Boundary guards matter because:
--   * `promote_request_logs_hot_to_partition` (function NAME) has the token
--     preceded by `_` — must not be touched;
--   * `request_logs_2026_10` child partitions (kept names) and
--     `'request_logs_' || to_char(...)` dynamic prefixes are followed by `_`
--     — must not be touched;
--   * `request_logs_with_current_month` views (kept names) are followed by
--     `_` — must not be touched.
-- The dot IS allowed as a preceding char here: inside SQL function bodies a
-- dot only appears as `public.` schema qualification, which must be rewritten.
DO $rewrite$
DECLARE
  r record;
  def text;
  tokens constant text[] := ARRAY[
    'request_logs_bodies_hot',
    'request_logs_bodies',
    'request_logs_hot',
    'request_logs_archive',
    'request_logs'
  ];
  tk text;
  n int := 0;
BEGIN
  FOR r IN
    SELECT p.oid AS fnoid, pg_get_functiondef(p.oid) AS def
      FROM pg_proc p
      JOIN pg_namespace nsp ON nsp.oid = p.pronamespace
     WHERE nsp.nspname = 'public'
       AND p.prosrc ~ '(?<![a-zA-Z0-9_])(request_logs(_bodies_hot|_bodies|_hot|_archive)?)(?![a-zA-Z0-9_])'
  LOOP
    def := r.def;
    FOREACH tk IN ARRAY tokens LOOP
      def := regexp_replace(
        def,
        '(?<![a-zA-Z0-9_])' || tk || '(?![a-zA-Z0-9_])',
        '_' || tk,
        'g'
      );
    END LOOP;
    EXECUTE def;
    n := n + 1;
  END LOOP;
  RAISE NOTICE 'migration 860: rewrote % function bodies referencing the request_logs family', n;
END
$rewrite$;

-- ── 3. Post-conditions ────────────────────────────────────────────────────
DO $assert$
DECLARE
  leftovers int;
BEGIN
  SELECT count(*) INTO leftovers
    FROM pg_proc p
    JOIN pg_namespace nsp ON nsp.oid = p.pronamespace
   WHERE nsp.nspname = 'public'
     AND p.prosrc ~ '(?<![a-zA-Z0-9_.])(request_logs(_bodies_hot|_bodies|_hot|_archive)?)(?![a-zA-Z0-9_])';
  IF leftovers > 0 THEN
    -- The dot is EXCLUDED here: a leftover `public.request_logs` would have
    -- been rewritten above; anything still matching is a form the rewrite
    -- pass could not see (e.g. SQL-built identifiers) and must be reviewed.
    RAISE EXCEPTION 'migration 860: % function bodies still reference unprefixed request_logs family names', leftovers;
  END IF;
  IF to_regclass('public._request_logs') IS NULL THEN
    RAISE EXCEPTION 'migration 860: public._request_logs missing after rename';
  END IF;
END
$assert$;

COMMIT;
