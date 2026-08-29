-- Migration: 614_session_bodies_hot
-- Purpose: Create session_bodies_hot table to align with hot+partition architecture
-- Related: docs/audit/2026-08-29-comprehensive-24h-audit.md P0 issue #1
-- Date: 2026-08-29

-- Create session_bodies_hot table with same structure as parent
CREATE TABLE IF NOT EXISTS public.session_bodies_hot (
    id bigint NOT NULL DEFAULT nextval('public.session_bodies_id_seq'::regclass),
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL
) WITH (fillfactor=90);

-- Add primary key
ALTER TABLE public.session_bodies_hot 
    ADD CONSTRAINT session_bodies_hot_pkey 
    PRIMARY KEY (id, partition_date);

-- Add unique constraint for upsert safety
ALTER TABLE public.session_bodies_hot 
    ADD CONSTRAINT session_bodies_hot_unique 
    UNIQUE (session_id, turn_no, tenant_id, partition_date);

-- Indexes for fast lookup
CREATE INDEX idx_session_bodies_hot_lookup 
    ON public.session_bodies_hot(session_id, turn_no, tenant_id);

CREATE INDEX idx_session_bodies_hot_request 
    ON public.session_bodies_hot(request_id, tenant_id);

CREATE INDEX idx_session_bodies_hot_ts 
    ON public.session_bodies_hot(ts) 
    WHERE ts > now() - interval '8 hours';

CREATE INDEX idx_session_bodies_hot_partition_date 
    ON public.session_bodies_hot(partition_date);

-- Add comment
COMMENT ON TABLE public.session_bodies_hot IS 
    'Hot table for session_bodies: 8-hour retention window before promotion to monthly partitions. All writes go here first.';

-- Create unified view that reads from hot + partitions
CREATE OR REPLACE VIEW public.session_bodies_unified AS
SELECT * FROM public.session_bodies_hot
UNION ALL
SELECT * FROM public.session_bodies
WHERE partition_date <= CURRENT_DATE - interval '1 day';

COMMENT ON VIEW public.session_bodies_unified IS 
    'Unified view reading from session_bodies_hot (last 8 hours) and session_bodies partitions (older data)';
