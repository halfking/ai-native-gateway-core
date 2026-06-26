-- ============================================================
-- Sync SQL for database: memos
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 1
-- ============================================================

\connect memos

CREATE TABLE public.doc_tools_tasks (
    task_id uuid DEFAULT gen_random_uuid() NOT NULL,
    task_type character varying(20) NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    user_id character varying(100) NOT NULL,
    source_filename character varying(500) NOT NULL,
    source_format character varying(20),
    target_format character varying(50) NOT NULL,
    target_language character varying(20),
    source_language character varying(20) DEFAULT 'auto'::character varying,
    preferred_provider character varying(50),
    mem_cube_id character varying(100),
    upload_id character varying(100),
    output_filename character varying(500),
    download_token character varying(100),
    error_message text,
    retry_count integer DEFAULT 0,
    max_retries integer DEFAULT 3,
    next_retry_at timestamp with time zone,
    last_error_type character varying(50),
    progress integer DEFAULT 0,
    logs jsonb DEFAULT '[]'::jsonb,
    translation_report jsonb,
    imported_to_kb boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone
);

CREATE INDEX idx_doc_tools_tasks_created ON public.doc_tools_tasks USING btree (created_at DESC);

CREATE INDEX idx_doc_tools_tasks_retry ON public.doc_tools_tasks USING btree (next_retry_at) WHERE ((status)::text = 'retrying'::text);

CREATE INDEX idx_doc_tools_tasks_status ON public.doc_tools_tasks USING btree (status);

CREATE INDEX idx_doc_tools_tasks_token ON public.doc_tools_tasks USING btree (download_token);

CREATE INDEX idx_doc_tools_tasks_user ON public.doc_tools_tasks USING btree (user_id);

CREATE TRIGGER trigger_doc_tools_tasks_updated_at BEFORE UPDATE ON public.doc_tools_tasks FOR EACH ROW EXECUTE FUNCTION public.update_doc_tools_tasks_updated_at();
