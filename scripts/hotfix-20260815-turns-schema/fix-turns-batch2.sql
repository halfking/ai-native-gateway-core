-- turns 依赖链第二批：464 + 474 + 465 + 471（全部幂等）
BEGIN;

-- 464: 聚合幂等标记列
ALTER TABLE public.session_turns
  ADD COLUMN IF NOT EXISTS aggregate_applied_at TIMESTAMPTZ;
COMMENT ON COLUMN public.session_turns.aggregate_applied_at IS
  'Timestamp at which this turn was atomically applied to public.sessions aggregate counters; NULL means pending/retryable.';

-- 474: 附件索引 + submit_mode CHECK（含 attachment_only）
CREATE INDEX IF NOT EXISTS idx_session_turns_multimodal_types_gw
  ON public.session_turns USING gin(multimodal_types)
  WHERE multimodal_types != '{}';
CREATE INDEX IF NOT EXISTS idx_session_turns_attachment_count_gw
  ON public.session_turns(attachment_count)
  WHERE attachment_count > 0;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='session_turns_submit_mode_check'
      AND conrelid='public.session_turns'::regclass
      AND pg_get_constraintdef(oid) LIKE '%attachment_only%'
  ) THEN
    ALTER TABLE public.session_turns
      DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;
    ALTER TABLE public.session_turns
      ADD CONSTRAINT session_turns_submit_mode_check
      CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'));
  END IF;
END $$;

COMMIT;

-- 465: session_titles 去重 + 主键（ON CONFLICT 依赖）
BEGIN;
DELETE FROM session_titles a USING session_titles b
WHERE a.ctid < b.ctid
  AND a.task_id = b.task_id
  AND a.scoped_session_id = b.scoped_session_id;
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='session_titles_pkey' AND conrelid='public.session_titles'::regclass
  ) THEN
    ALTER TABLE public.session_titles
      ADD CONSTRAINT session_titles_pkey PRIMARY KEY (task_id, scoped_session_id);
  END IF;
END $$;
COMMIT;

-- 471: session_summaries 归档列
BEGIN;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries' AND column_name='last_accessed_at') THEN
    ALTER TABLE public.session_summaries ADD COLUMN last_accessed_at TIMESTAMPTZ;
    UPDATE public.session_summaries SET last_accessed_at = last_request_at WHERE last_accessed_at IS NULL;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries' AND column_name='archived_at') THEN
    ALTER TABLE public.session_summaries ADD COLUMN archived_at TIMESTAMPTZ;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes
    WHERE schemaname='public' AND tablename='session_summaries' AND indexname='idx_session_summaries_archival') THEN
    CREATE INDEX idx_session_summaries_archival
      ON public.session_summaries (archived_at, last_accessed_at, last_request_at)
      WHERE archived_at IS NULL;
  END IF;
END $$;
COMMIT;
SELECT 'batch2 ok';
