-- 953_models_canonical_featured.down.sql
-- Rollback: Remove featured column from models_canonical

DO $$ 
BEGIN
    -- Drop index if exists
    IF EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
          AND tablename = 'models_canonical' 
          AND indexname = 'idx_models_canonical_featured'
    ) THEN
        DROP INDEX idx_models_canonical_featured;
        RAISE NOTICE 'Dropped idx_models_canonical_featured index';
    END IF;
    
    -- Drop column if exists
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_schema = 'public' 
          AND table_name = 'models_canonical' 
          AND column_name = 'featured'
    ) THEN
        ALTER TABLE models_canonical DROP COLUMN featured;
        RAISE NOTICE 'Dropped models_canonical.featured column';
    END IF;
END $$;
