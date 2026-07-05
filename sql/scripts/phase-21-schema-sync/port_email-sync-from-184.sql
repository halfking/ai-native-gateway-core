-- ============================================================
-- Sync SQL for database: port_email
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 7
-- ============================================================

\connect port_email

CREATE TABLE public.port_agent_owners (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id character varying(100),
    owner_id character varying(100),
    owner_name character varying(200),
    owner_email character varying(255),
    notify_enabled boolean DEFAULT true,
    created_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_audit_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    email_id text,
    task_flow_id uuid,
    action character varying(100),
    actor character varying(100),
    details jsonb,
    created_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_human_confirmations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    node_id uuid,
    action character varying(50),
    original_data jsonb,
    modified_data jsonb,
    comment text,
    confirmed_by character varying(100),
    confirmed_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    type character varying(50),
    recipient character varying(100),
    title character varying(200),
    content text,
    status character varying(50) DEFAULT 'pending'::character varying,
    sent_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_skill_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    skill_id character varying(100),
    version character varying(50),
    prompt_template text,
    optimization_notes text,
    changes_summary text,
    improvement_metrics jsonb,
    optimized_by character varying(100),
    source character varying(50),
    created_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_task_flows (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    email_id text,
    workflow_type character varying(50),
    status character varying(50) DEFAULT 'pending'::character varying,
    current_node character varying(100),
    planned_end_at timestamp without time zone,
    actual_end_at timestamp without time zone,
    owner_id character varying(100),
    created_at timestamp without time zone DEFAULT now(),
    updated_at timestamp without time zone DEFAULT now()
);

CREATE TABLE public.port_task_nodes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    task_flow_id uuid,
    node_id character varying(100),
    node_name character varying(200),
    status character varying(50) DEFAULT 'pending'::character varying,
    planned_at timestamp without time zone,
    started_at timestamp without time zone,
    completed_at timestamp without time zone,
    result jsonb,
    confirmed_by character varying(100),
    confirmed_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT now()
);

CREATE INDEX idx_port_agent_owners_agent_id ON public.port_agent_owners USING btree (agent_id);

CREATE INDEX idx_port_audit_logs_action ON public.port_audit_logs USING btree (action);

CREATE INDEX idx_port_audit_logs_created_at ON public.port_audit_logs USING btree (created_at);

CREATE INDEX idx_port_audit_logs_email_id ON public.port_audit_logs USING btree (email_id);

CREATE INDEX idx_port_audit_logs_tenant_id ON public.port_audit_logs USING btree (tenant_id);

CREATE INDEX idx_port_skill_versions_skill_id ON public.port_skill_versions USING btree (skill_id);

CREATE INDEX idx_port_task_flows_email_id ON public.port_task_flows USING btree (email_id);

CREATE INDEX idx_port_task_flows_status ON public.port_task_flows USING btree (status);

CREATE INDEX idx_port_task_flows_tenant_id ON public.port_task_flows USING btree (tenant_id);

CREATE INDEX idx_port_task_nodes_status ON public.port_task_nodes USING btree (status);

CREATE INDEX idx_port_task_nodes_task_flow_id ON public.port_task_nodes USING btree (task_flow_id);
