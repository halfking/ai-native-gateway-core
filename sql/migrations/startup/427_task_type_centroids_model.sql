-- 427_task_type_centroids_model.sql
-- Isolate centroids by embedding model to prevent incompatible vector spaces
-- from being compared or updated together.

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.task_type_centroids') IS NOT NULL THEN
        ALTER TABLE public.task_type_centroids
            ADD COLUMN IF NOT EXISTS embedding_model text NOT NULL DEFAULT '';

        ALTER TABLE public.task_type_centroids
            DROP CONSTRAINT IF EXISTS task_type_centroids_pkey;

        ALTER TABLE public.task_type_centroids
            ADD CONSTRAINT task_type_centroids_pkey PRIMARY KEY (task_type, embedding_model);

        CREATE INDEX IF NOT EXISTS idx_task_type_centroids_model
            ON public.task_type_centroids (embedding_model, task_type);
    ELSE
        RAISE NOTICE 'task_type_centroids is unavailable; model isolation migration skipped';
    END IF;
END $$;

COMMIT;
