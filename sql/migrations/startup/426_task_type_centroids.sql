-- 426_task_type_centroids.sql
-- M3 embedding semantic routing shadow centroids (22 章 §22.7).

BEGIN;

DO $$
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS vector;
    EXCEPTION
        WHEN OTHERS THEN
            RAISE NOTICE 'pgvector unavailable; task_type_centroids migration skipped: %', SQLERRM;
    END;

    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
        BEGIN
            EXECUTE $sql$
                CREATE TABLE IF NOT EXISTS public.task_type_centroids (
                    task_type    text PRIMARY KEY,
                    centroid     vector(1024) NOT NULL,
                    sample_count integer NOT NULL DEFAULT 0,
                    updated_at   timestamp with time zone NOT NULL DEFAULT now()
                )
            $sql$;
            EXECUTE $sql$
                CREATE INDEX IF NOT EXISTS idx_task_type_centroids_embedding
                    ON public.task_type_centroids
                    USING ivfflat (centroid vector_cosine_ops) WITH (lists = 10)
            $sql$;
        EXCEPTION
            WHEN OTHERS THEN
                RAISE NOTICE 'task_type_centroids setup skipped: %', SQLERRM;
        END;
    ELSE
        RAISE NOTICE 'pgvector extension is unavailable; task_type_centroids setup skipped';
    END IF;
END $$;

COMMIT;
