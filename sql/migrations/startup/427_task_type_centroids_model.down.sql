-- 427_task_type_centroids_model.down.sql
-- Restore the M3 centroid key to task_type-only semantics.

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.task_type_centroids') IS NOT NULL THEN
        DELETE FROM public.task_type_centroids
            WHERE embedding_model <> '';
        RAISE NOTICE '427 rollback removed model-specific centroids; legacy task_type rows retained';

        ALTER TABLE public.task_type_centroids
            DROP CONSTRAINT IF EXISTS task_type_centroids_pkey;

        ALTER TABLE public.task_type_centroids
            ADD CONSTRAINT task_type_centroids_pkey PRIMARY KEY (task_type);

        ALTER TABLE public.task_type_centroids
            DROP COLUMN IF EXISTS embedding_model;
    ELSE
        RAISE NOTICE 'task_type_centroids is unavailable; model isolation rollback skipped';
    END IF;
END $$;

COMMIT;
