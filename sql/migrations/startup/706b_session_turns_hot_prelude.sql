-- Migration 706b: create public.session_turns_hot before 707 widens it.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- 707_session_turns_s1a.sql does
--     ALTER TABLE public.session_turns_hot ...
-- but session_turns_hot is created by 526_session_turns_hot.sql, which is
-- deliberately NOT in this sequence — the comment above the 706/707/708
-- block states that on a normal host the gateway's Go ensure chain creates
-- the hot table during first boot, so the sequence must not. This sequence
-- runs BEFORE that chain has ever booted on a host-first deploy, so 707
-- aborted with
--     ERROR: relation "public.session_turns_hot" does not exist
-- and the ledger never advanced, blocking every later file.
--
-- Registering 526 itself does not work. It is one transaction ending in a
-- hard mirror contract plus a post-condition that requires eleven named
-- constraints and a set of named indexes. Its hardcoded CREATE TABLE column
-- list also predates 706's widening of the PARENT, so whichever order is
-- chosen one of the two checks fails on a fresh database.
--
-- So this file creates the table the way 526 means it, derived from the live
-- parent catalog so the shapes cannot drift:
--   * CREATE TABLE ... LIKE public.session_turns INCLUDING ALL copies column
--     names, types, defaults, NOT NULL, identity and storage parameters, and
--     (INCLUDING ALL implies INCLUDING CONSTRAINTS + INCLUDING INDEXES) the
--     parent's CHECK / UNIQUE constraints and indexes -- under names derived
--     from the PARENT table.
--   * Those copied constraints are therefore RENAMED below (see the constraint
--     block), not re-created: re-adding would fail with "multiple primary
--     keys".
--     ★★ 2026-10-28（R175 轮）订正：★★ 本段原先写的是「LIKE does not copy
--     UNIQUE/PRIMARY KEY or CHECK constraints, so those are re-created
--     explicitly, with the same names and definitions 526 declares」——
--     ★★ **那是错的**：★★ PG 的 INCLUDING ALL 含 INCLUDING CONSTRAINTS
--     ⇒ ★ CHECK / UNIQUE **会被复制**（名字取自父表）⇒ ★★ 与本文件下方
--     第 49 行起的实际做法（**改名**，而不是重建）**自相矛盾**；
--     ★★ 而 R175 的迁移 914 把「**两侧都要加**」这条范式**建立在这个真实行为上**
--     ⇒ ★★ 这处订正不是文字游戏：★ 它决定着 914 该不该给 hot 表也加一条约束。
--
-- The table is empty on a fresh deploy, so creating it here is lossless. On
-- a database where it already exists this file is a no-op and 526's own
-- contract check (which runs when the Go chain gets there) decides.
--
-- Idempotent: guarded by an existence check on the table.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'session_turns_hot'
  ) THEN
    RAISE NOTICE '706b: public.session_turns_hot already exists; nothing to do';
    RETURN;
  END IF;

  RAISE NOTICE '706b: creating public.session_turns_hot from the current session_turns shape';

  EXECUTE 'CREATE TABLE public.session_turns_hot (LIKE public.session_turns INCLUDING ALL) WITH (fillfactor = 90)';

  -- Constraints. INCLUDING ALL already copies the parent's PRIMARY KEY, its
  -- UNIQUE constraints and its CHECK constraints, but under names derived
  -- from the PARENT table (session_turns_* / auto-generated unique names).
  -- 526's post-condition looks them up by 526's own names, so rename rather
  -- than re-add — re-adding would fail with "multiple primary keys".
  -- Mapping below is derived from the live catalog, not guessed.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'public.session_turns_hot'::regclass
      AND conname = 'session_turns_hot_tenant_session_turn_key'
  ) THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot
               RENAME CONSTRAINT session_turns_hot_tenant_id_session_id_turn_no_partition_da_key
               TO session_turns_hot_tenant_session_turn_key';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'public.session_turns_hot'::regclass
      AND conname = 'session_turns_hot_tenant_request_key'
  ) THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot
               RENAME CONSTRAINT session_turns_hot_tenant_id_request_id_partition_date_key
               TO session_turns_hot_tenant_request_key';
  END IF;

  -- CHECK constraints: rename the parent's names to 526's hot-side names so
  -- the post-condition list matches. Each rename is skipped when the target
  -- name already exists, which is what makes re-runs no-ops.
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_submit_mode_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_submit_mode_check TO session_turns_hot_submit_mode_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_injection_verdict_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_injection_verdict_check TO session_turns_hot_injection_verdict_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_output_verdict_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_output_verdict_check TO session_turns_hot_output_verdict_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_source_kind_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_source_kind_check TO session_turns_hot_source_kind_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_quality_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_quality_check TO session_turns_hot_quality_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_attachment_count_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_attachment_count_check TO session_turns_hot_attachment_count_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_attachment_total_bytes_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_attachment_total_bytes_check TO session_turns_hot_attachment_total_bytes_check';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns_hot'::regclass AND conname='session_turns_hot_attempt_no_check') THEN
    EXECUTE 'ALTER TABLE public.session_turns_hot RENAME CONSTRAINT session_turns_attempt_no_check TO session_turns_hot_attempt_no_check';
  END IF;

  EXECUTE 'COMMENT ON TABLE public.session_turns_hot IS
             ''Independent heap write table for recent session turns. Cold rows are atomically moved to public.session_turns by promote_session_turns_hot_to_partition().''';
END
$$;
