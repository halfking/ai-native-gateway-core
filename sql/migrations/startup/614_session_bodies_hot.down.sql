-- Rollback: 614_session_bodies_hot
-- Purpose: Drop session_bodies_hot table and unified view

DROP VIEW IF EXISTS public.session_bodies_unified;
DROP TABLE IF EXISTS public.session_bodies_hot;
