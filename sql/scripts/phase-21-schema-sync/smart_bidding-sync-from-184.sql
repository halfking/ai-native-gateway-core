-- ============================================================
-- Sync SQL for database: smart_bidding
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 8
-- ============================================================

\connect smart_bidding

CREATE TABLE public.smart_bidding_audit_logs (
    id character varying(64) NOT NULL,
    user_id character varying(64) NOT NULL,
    username character varying(100) NOT NULL,
    action character varying(64) NOT NULL,
    resource_type character varying(64) NOT NULL,
    resource_id character varying(64) NOT NULL,
    details text DEFAULT ''::text NOT NULL,
    ip_address character varying(64) DEFAULT ''::character varying NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_chapter_versions (
    id character varying(64) NOT NULL,
    chapter_id character varying(64) NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    version integer NOT NULL,
    change_summary text,
    created_by character varying(64) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_chapters (
    id character varying(64) NOT NULL,
    project_id character varying(64) NOT NULL,
    parent_id character varying(64),
    title character varying(500) NOT NULL,
    content text,
    chapter_number character varying(32) DEFAULT '1'::character varying NOT NULL,
    level integer DEFAULT 1 NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    lock_user_id character varying(64),
    lock_username character varying(100),
    locked_at timestamp with time zone,
    score integer DEFAULT 0 NOT NULL,
    word_count integer DEFAULT 0 NOT NULL,
    ai_word_count integer DEFAULT 0 NOT NULL,
    humanized_words integer DEFAULT 0 NOT NULL,
    is_completed boolean DEFAULT false NOT NULL,
    template_id character varying(64),
    created_by character varying(64) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_image_library (
    id character varying(64) NOT NULL,
    project_id character varying(64) NOT NULL,
    file_name character varying(500) NOT NULL,
    file_path text NOT NULL,
    file_size bigint DEFAULT 0 NOT NULL,
    mime_type character varying(128) DEFAULT ''::character varying NOT NULL,
    width integer DEFAULT 0 NOT NULL,
    height integer DEFAULT 0 NOT NULL,
    uploaded_by character varying(64) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_notifications (
    id character varying(64) NOT NULL,
    user_id character varying(64) NOT NULL,
    type character varying(64) NOT NULL,
    title character varying(255) NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    is_read boolean DEFAULT false NOT NULL,
    related_id character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_projects (
    id character varying(64) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    client_name character varying(255),
    project_type character varying(64) DEFAULT 'general'::character varying NOT NULL,
    status character varying(32) DEFAULT 'draft'::character varying NOT NULL,
    owner_id character varying(64) NOT NULL,
    deadline timestamp with time zone,
    watermark_text text,
    total_chapters integer DEFAULT 0 NOT NULL,
    completed_score integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.smart_bidding_templates (
    id character varying(64) NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    content text DEFAULT ''::text NOT NULL,
    category character varying(64) DEFAULT 'general'::character varying NOT NULL,
    is_built_in boolean DEFAULT false NOT NULL,
    created_by character varying(64) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.users (
    id character varying(64) NOT NULL,
    name character varying(100) NOT NULL,
    email character varying(255) DEFAULT ''::character varying,
    role character varying(32) DEFAULT 'writer'::character varying NOT NULL,
    password_hash character varying(128) NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE INDEX idx_sbal_action ON public.smart_bidding_audit_logs USING btree (action);

CREATE INDEX idx_sbal_created ON public.smart_bidding_audit_logs USING btree (created_at);

CREATE INDEX idx_sbal_user ON public.smart_bidding_audit_logs USING btree (user_id);

CREATE INDEX idx_sbc_parent ON public.smart_bidding_chapters USING btree (parent_id);

CREATE INDEX idx_sbc_project ON public.smart_bidding_chapters USING btree (project_id);

CREATE INDEX idx_sbcv_chapter ON public.smart_bidding_chapter_versions USING btree (chapter_id);

CREATE INDEX idx_sbil_project ON public.smart_bidding_image_library USING btree (project_id);

CREATE INDEX idx_sbn_read ON public.smart_bidding_notifications USING btree (is_read);

CREATE INDEX idx_sbn_user ON public.smart_bidding_notifications USING btree (user_id);

CREATE INDEX idx_sbp_owner ON public.smart_bidding_projects USING btree (owner_id);

CREATE INDEX idx_sbp_status ON public.smart_bidding_projects USING btree (status);

CREATE INDEX idx_sbt_category ON public.smart_bidding_templates USING btree (category);
