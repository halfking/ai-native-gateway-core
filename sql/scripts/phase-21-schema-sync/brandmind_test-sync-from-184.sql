-- ============================================================
-- Sync SQL for database: brandmind_test
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 28
-- ============================================================

\connect brandmind_test

CREATE TABLE public._brandmind_schema_migrations (
    filename text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.alerts (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    type character varying(50) NOT NULL,
    severity character varying(10) NOT NULL,
    title character varying(512) NOT NULL,
    description text DEFAULT ''::text,
    data jsonb DEFAULT '{}'::jsonb,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    resolved_at timestamp with time zone,
    acknowledged_at timestamp with time zone,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT alerts_severity_check CHECK (((severity)::text = ANY (ARRAY[('low'::character varying)::text, ('medium'::character varying)::text, ('high'::character varying)::text]))),
    CONSTRAINT alerts_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('acknowledged'::character varying)::text, ('resolved'::character varying)::text]))),
    CONSTRAINT alerts_type_check CHECK (((type)::text = ANY (ARRAY[('cognition_drift'::character varying)::text, ('competitor_surge'::character varying)::text, ('brand_crisis'::character varying)::text, ('content_gap'::character varying)::text])))
);

CREATE TABLE public.brands (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    name character varying(255) NOT NULL,
    industry character varying(255) NOT NULL,
    website character varying(512),
    competitors jsonb DEFAULT '[]'::jsonb,
    keywords jsonb DEFAULT '[]'::jsonb,
    description text DEFAULT ''::text,
    config jsonb DEFAULT '{"platforms": ["deepseek", "doubao", "yuanbao", "kimi"], "probeSchedule": {"aiScore": "daily", "contentCoverage": "every12h", "competitorCompare": "weekly"}, "maxCompetitors": 5, "alertSensitivity": "standard"}'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    geoflow_config jsonb DEFAULT '{}'::jsonb,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    logo_url character varying(512),
    product_images jsonb DEFAULT '[]'::jsonb,
    target_audience text DEFAULT ''::text,
    slogan character varying(255) DEFAULT ''::character varying,
    contact_info jsonb DEFAULT '{}'::jsonb
);

COMMENT ON COLUMN public.brands.geoflow_config IS 'GEOFlow 生成配置
{
  "titleLibraryId": 5,
  "knowledgeBaseId": 12,
  "aiModelId": 3,
  "promptId": 7,
  "publishIntervalMinutes": 1440,
  "distributionChannelIds": [1, 3],
  "needReview": true,
  "autoGenerateOnProbe": false
}';

CREATE TABLE public.channel_weights (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    platform character varying(50) NOT NULL,
    weight numeric(3,2) DEFAULT 0.5 NOT NULL,
    expected_roi numeric(5,2) DEFAULT 0,
    actual_roi numeric(5,2) DEFAULT 0,
    week_of date NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.content_jobs (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    topic character varying(512) NOT NULL,
    keywords jsonb DEFAULT '[]'::jsonb,
    platforms jsonb DEFAULT '[]'::jsonb,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    results jsonb DEFAULT '[]'::jsonb,
    error text,
    created_at timestamp with time zone DEFAULT now(),
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT content_jobs_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('running'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('partial'::character varying)::text])))
);

CREATE TABLE public.crm_leads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    source_type character varying(20) NOT NULL,
    source_id uuid,
    source_url text,
    company_name character varying(255),
    contact_name character varying(128),
    contact_info jsonb DEFAULT '{}'::jsonb,
    behavior_data jsonb DEFAULT '{}'::jsonb,
    lead_score integer DEFAULT 0 NOT NULL,
    status character varying(20) DEFAULT 'new'::character varying NOT NULL,
    acc_task_id character varying(128),
    first_contacted_at timestamp with time zone,
    converted_at timestamp with time zone,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_leads_source_type_check CHECK (((source_type)::text = ANY (ARRAY[('published_content'::character varying)::text, ('social_engagement'::character varying)::text, ('manual'::character varying)::text, ('other'::character varying)::text]))),
    CONSTRAINT crm_leads_status_check CHECK (((status)::text = ANY (ARRAY[('new'::character varying)::text, ('qualifying'::character varying)::text, ('qualified'::character varying)::text, ('contacted'::character varying)::text, ('converted'::character varying)::text, ('lost'::character varying)::text])))
);

CREATE TABLE public.daily_briefs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    brief_date date NOT NULL,
    content jsonb NOT NULL,
    markdown text NOT NULL,
    push_status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    push_channel character varying(50),
    push_error text,
    pushed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT daily_briefs_push_status_check CHECK (((push_status)::text = ANY (ARRAY[('pending'::character varying)::text, ('pushed'::character varying)::text, ('failed'::character varying)::text, ('skipped'::character varying)::text])))
);

CREATE TABLE public.geo_reports (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    overall_score integer NOT NULL,
    overall_grade character varying(2) NOT NULL,
    executive_summary text,
    advices jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying
);

CREATE TABLE public.industry_benchmarks (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    industry character varying(255) NOT NULL,
    metric character varying(100) NOT NULL,
    value numeric(10,4) NOT NULL,
    sample_size integer DEFAULT 1 NOT NULL,
    period date NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying
);

CREATE TABLE public.ingest_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    query text,
    keywords text[] DEFAULT '{}'::text[],
    sources text[] DEFAULT '{}'::text[],
    trendaradar_task_id text,
    status text DEFAULT 'pending'::text NOT NULL,
    item_count integer DEFAULT 0,
    summary jsonb DEFAULT '{}'::jsonb,
    error text,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying
);

CREATE TABLE public.knowledge_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    ingest_job_id uuid,
    source text NOT NULL,
    source_url text,
    title text NOT NULL,
    summary text,
    content text,
    author text,
    tags text[] DEFAULT '{}'::text[],
    category text DEFAULT 'other'::text,
    relevance_score numeric(4,2) DEFAULT 0,
    raw jsonb DEFAULT '{}'::jsonb,
    published_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying
);

CREATE TABLE public.lead_followups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    lead_id uuid NOT NULL,
    action_type character varying(30) NOT NULL,
    content text,
    acc_task_id character varying(128),
    next_action_at timestamp with time zone,
    performed_by uuid,
    status character varying(20) DEFAULT 'completed'::character varying NOT NULL,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lead_followups_action_type_check CHECK (((action_type)::text = ANY (ARRAY[('auto_message'::character varying)::text, ('manual_call'::character varying)::text, ('email'::character varying)::text, ('meeting'::character varying)::text, ('note'::character varying)::text, ('status_change'::character varying)::text]))),
    CONSTRAINT lead_followups_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('skipped'::character varying)::text])))
);

CREATE TABLE public.notification_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid,
    title character varying(255) NOT NULL,
    content text NOT NULL,
    content_hash character varying(64) NOT NULL,
    channel character varying(20) NOT NULL,
    message_type character varying(30) NOT NULL,
    recipient_id uuid,
    recipient_info character varying(512),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    last_error text,
    upstream_id character varying(128),
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_log_channel_check CHECK (((channel)::text = ANY (ARRAY[('feishu'::character varying)::text, ('email'::character varying)::text, ('dingtalk'::character varying)::text, ('wework'::character varying)::text, ('telegram'::character varying)::text, ('ntfy'::character varying)::text]))),
    CONSTRAINT notification_log_message_type_check CHECK (((message_type)::text = ANY (ARRAY[('daily_brief'::character varying)::text, ('weekly_plan'::character varying)::text, ('monthly_report'::character varying)::text, ('alert'::character varying)::text, ('gate_approval'::character varying)::text, ('lead_notification'::character varying)::text, ('owner_report'::character varying)::text, ('other'::character varying)::text]))),
    CONSTRAINT notification_log_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('sent'::character varying)::text, ('failed'::character varying)::text, ('retrying'::character varying)::text, ('skipped'::character varying)::text])))
);

CREATE TABLE public.notification_preferences (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    brand_id uuid,
    message_type character varying(30) NOT NULL,
    enabled_channels jsonb DEFAULT '["feishu"]'::jsonb NOT NULL,
    push_window jsonb DEFAULT '{"end": "22:00", "start": "08:00"}'::jsonb,
    is_enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.ooda_cycles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    cycle_type character varying(20) DEFAULT 'daily'::character varying NOT NULL,
    phase character varying(20) DEFAULT 'observe'::character varying NOT NULL,
    status character varying(20) DEFAULT 'in_progress'::character varying NOT NULL,
    input_data jsonb DEFAULT '{}'::jsonb,
    output_data jsonb DEFAULT '{}'::jsonb,
    acc_task_id character varying(128),
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ooda_cycles_cycle_type_check CHECK (((cycle_type)::text = ANY (ARRAY[('daily'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text]))),
    CONSTRAINT ooda_cycles_status_check CHECK (((status)::text = ANY (ARRAY[('in_progress'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('skipped'::character varying)::text])))
);

CREATE TABLE public.ooda_schedule_state (
    brand_id uuid NOT NULL,
    last_daily_at timestamp with time zone,
    last_weekly_at timestamp with time zone,
    next_daily_at timestamp with time zone,
    next_weekly_at timestamp with time zone,
    is_paused boolean DEFAULT false NOT NULL,
    pause_reason text,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.operation_logs (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    user_id uuid,
    brand_id uuid,
    action character varying(64) NOT NULL,
    target_type character varying(64),
    target_id character varying(128),
    platform character varying(50),
    status character varying(20) DEFAULT 'success'::character varying NOT NULL,
    details jsonb DEFAULT '{}'::jsonb,
    error text,
    duration_ms integer,
    ip character varying(45),
    user_agent character varying(512),
    created_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT operation_logs_status_check CHECK (((status)::text = ANY (ARRAY[('success'::character varying)::text, ('failure'::character varying)::text, ('partial'::character varying)::text, ('skipped'::character varying)::text])))
);

CREATE TABLE public.platform_accounts (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    platform character varying(50) NOT NULL,
    username character varying(255) NOT NULL,
    password_encrypted text,
    cookies_encrypted text,
    profile_url character varying(1024),
    nickname character varying(255),
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    last_used_at timestamp with time zone,
    notes text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT platform_accounts_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('expired'::character varying)::text, ('banned'::character varying)::text, ('pending_verify'::character varying)::text])))
);

CREATE TABLE public.probe_details (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    probe_id uuid NOT NULL,
    platform character varying(50) NOT NULL,
    type character varying(20) NOT NULL,
    query text NOT NULL,
    raw_response text NOT NULL,
    score smallint DEFAULT 0 NOT NULL,
    extracted_info jsonb DEFAULT '{}'::jsonb,
    tokens_used integer DEFAULT 0,
    model character varying(100),
    latency_ms integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    CONSTRAINT probe_details_score_check CHECK (((score >= 0) AND (score <= 100))),
    CONSTRAINT probe_details_type_check CHECK (((type)::text = ANY (ARRAY[('recommendation'::character varying)::text, ('cognition'::character varying)::text])))
);

CREATE TABLE public.probe_results (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    probe_date timestamp with time zone DEFAULT now() NOT NULL,
    recommendation_score smallint DEFAULT 0 NOT NULL,
    cognition_score smallint DEFAULT 0 NOT NULL,
    overall_score smallint DEFAULT 0 NOT NULL,
    snapshot_date date DEFAULT CURRENT_DATE NOT NULL,
    details jsonb DEFAULT '[]'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    geo_analysis jsonb DEFAULT '{}'::jsonb,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT probe_results_cognition_score_check CHECK (((cognition_score >= 0) AND (cognition_score <= 100))),
    CONSTRAINT probe_results_overall_score_check CHECK (((overall_score >= 0) AND (overall_score <= 100))),
    CONSTRAINT probe_results_recommendation_score_check CHECK (((recommendation_score >= 0) AND (recommendation_score <= 100)))
);

COMMENT ON COLUMN public.probe_results.geo_analysis IS 'GEO 分析附加数据
{
  "recommended_method": "yao-geo-knowledge-base-builder",
  "content_gaps": ["缺少FAQ页面", "品牌描述不准确"],
  "urgency": "high"
}';

CREATE TABLE public.publish_tasks (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    platform character varying(50) NOT NULL,
    account_id uuid,
    job_id uuid,
    title character varying(512) NOT NULL,
    content text NOT NULL,
    mode character varying(20) DEFAULT 'rpa'::character varying NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    published_url character varying(1024),
    error text,
    rpa_log jsonb DEFAULT '[]'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT publish_tasks_mode_check CHECK (((mode)::text = ANY (ARRAY[('rpa'::character varying)::text, ('manual'::character varying)::text]))),
    CONSTRAINT publish_tasks_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('running'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('manual_required'::character varying)::text])))
);

CREATE TABLE public.published_contents (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    platform character varying(50) NOT NULL,
    title character varying(512) NOT NULL,
    url character varying(1024) NOT NULL,
    content_excerpt text DEFAULT ''::text,
    job_id uuid,
    published_at timestamp with time zone DEFAULT now() NOT NULL,
    metrics jsonb DEFAULT '{}'::jsonb,
    last_metrics_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying
);

CREATE TABLE public.reports (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    brand_id uuid NOT NULL,
    type character varying(20) NOT NULL,
    period_start date NOT NULL,
    period_end date NOT NULL,
    summary text DEFAULT ''::text,
    data jsonb DEFAULT '{}'::jsonb,
    pdf_url character varying(512),
    created_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT reports_type_check CHECK (((type)::text = ANY (ARRAY[('weekly'::character varying)::text, ('monthly'::character varying)::text])))
);

CREATE TABLE public.strategy_goals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    goal_type character varying(30) NOT NULL,
    target_desc text NOT NULL,
    target_metrics jsonb DEFAULT '{}'::jsonb NOT NULL,
    deadline date,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    progress integer DEFAULT 0 NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT strategy_goals_goal_type_check CHECK (((goal_type)::text = ANY (ARRAY[('visibility'::character varying)::text, ('sentiment'::character varying)::text, ('coverage'::character varying)::text, ('conversion'::character varying)::text, ('custom'::character varying)::text]))),
    CONSTRAINT strategy_goals_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('achieved'::character varying)::text, ('expired'::character varying)::text, ('cancelled'::character varying)::text])))
);

CREATE TABLE public.strategy_plans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    goal_id uuid,
    plan_type character varying(20) DEFAULT 'initial'::character varying NOT NULL,
    plan_data jsonb NOT NULL,
    confidence numeric(3,2) DEFAULT 0.5,
    risks jsonb DEFAULT '[]'::jsonb,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    approved_by uuid,
    approved_at timestamp with time zone,
    adjustment_note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT strategy_plans_plan_type_check CHECK (((plan_type)::text = ANY (ARRAY[('initial'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text, ('adjustment'::character varying)::text]))),
    CONSTRAINT strategy_plans_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('approved'::character varying)::text, ('adjusted'::character varying)::text, ('rejected'::character varying)::text, ('executing'::character varying)::text, ('completed'::character varying)::text])))
);

CREATE TABLE public.subscriptions (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    plan character varying(20) NOT NULL,
    brand_limit integer DEFAULT 10 NOT NULL,
    monthly_probe_quota integer DEFAULT 500 NOT NULL,
    probe_used integer DEFAULT 0 NOT NULL,
    period_start date NOT NULL,
    period_end date NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT subscriptions_plan_check CHECK (((plan)::text = ANY (ARRAY[('smb'::character varying)::text, ('enterprise'::character varying)::text])))
);

CREATE TABLE public.users (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    casdoor_id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    email character varying(512),
    role character varying(20) DEFAULT 'operator'::character varying NOT NULL,
    brand_ids jsonb DEFAULT '[]'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tenant_id character varying(100) DEFAULT 'kaixuan'::character varying,
    CONSTRAINT users_role_check CHECK (((role)::text = ANY (ARRAY[('owner'::character varying)::text, ('strategist'::character varying)::text, ('operator'::character varying)::text])))
);

CREATE TABLE public.weekly_plans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    brand_id uuid NOT NULL,
    week_start date NOT NULL,
    plan_data jsonb NOT NULL,
    weekly_review jsonb DEFAULT '{}'::jsonb,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    approved_by uuid,
    approved_at timestamp with time zone,
    rejection_reason text,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT weekly_plans_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('approved'::character varying)::text, ('adjusted'::character varying)::text, ('rejected'::character varying)::text])))
);

CREATE INDEX idx_alert_brand ON public.alerts USING btree (brand_id);

CREATE INDEX idx_alert_created ON public.alerts USING btree (created_at DESC);

CREATE INDEX idx_alert_severity ON public.alerts USING btree (severity);

CREATE INDEX idx_alert_status ON public.alerts USING btree (status);

CREATE INDEX idx_alerts_tenant ON public.alerts USING btree (tenant_id);

CREATE INDEX idx_benchmark_industry ON public.industry_benchmarks USING btree (industry);

CREATE INDEX idx_benchmark_metric ON public.industry_benchmarks USING btree (metric);

CREATE UNIQUE INDEX idx_benchmark_unique ON public.industry_benchmarks USING btree (industry, metric, period);

CREATE INDEX idx_brands_industry ON public.brands USING btree (industry);

CREATE INDEX idx_brands_name ON public.brands USING btree (name);

CREATE INDEX idx_brands_tenant ON public.brands USING btree (tenant_id);

CREATE INDEX idx_channel_weight_brand ON public.channel_weights USING btree (brand_id);

CREATE INDEX idx_channel_weight_roi ON public.channel_weights USING btree (actual_roi DESC);

CREATE INDEX idx_channel_weight_week ON public.channel_weights USING btree (week_of DESC);

CREATE INDEX idx_content_job_brand ON public.content_jobs USING btree (brand_id);

CREATE INDEX idx_content_job_date ON public.content_jobs USING btree (created_at DESC);

CREATE INDEX idx_content_job_status ON public.content_jobs USING btree (status);

CREATE INDEX idx_content_jobs_tenant ON public.content_jobs USING btree (tenant_id);

CREATE INDEX idx_crm_lead_acc_task ON public.crm_leads USING btree (acc_task_id);

CREATE INDEX idx_crm_lead_brand ON public.crm_leads USING btree (brand_id);

CREATE INDEX idx_crm_lead_created ON public.crm_leads USING btree (created_at DESC);

CREATE INDEX idx_crm_lead_score ON public.crm_leads USING btree (lead_score DESC);

CREATE INDEX idx_crm_lead_status ON public.crm_leads USING btree (status);

CREATE INDEX idx_daily_brief_brand ON public.daily_briefs USING btree (brand_id);

CREATE INDEX idx_daily_brief_date ON public.daily_briefs USING btree (brief_date DESC);

CREATE INDEX idx_daily_brief_status ON public.daily_briefs USING btree (push_status);

CREATE INDEX idx_detail_platform ON public.probe_details USING btree (platform);

CREATE INDEX idx_detail_probe_id ON public.probe_details USING btree (probe_id);

CREATE INDEX idx_followup_action ON public.lead_followups USING btree (action_type);

CREATE INDEX idx_followup_created ON public.lead_followups USING btree (created_at DESC);

CREATE INDEX idx_followup_lead ON public.lead_followups USING btree (lead_id);

CREATE INDEX idx_followup_next ON public.lead_followups USING btree (next_action_at) WHERE (next_action_at IS NOT NULL);

CREATE INDEX idx_geo_reports_brand ON public.geo_reports USING btree (brand_id);

CREATE INDEX idx_geo_reports_tenant ON public.geo_reports USING btree (tenant_id);

CREATE INDEX idx_industry_benchmarks_tenant ON public.industry_benchmarks USING btree (tenant_id);

CREATE INDEX idx_ingest_jobs_brand ON public.ingest_jobs USING btree (brand_id);

CREATE INDEX idx_ingest_jobs_created ON public.ingest_jobs USING btree (created_at DESC);

CREATE INDEX idx_ingest_jobs_status ON public.ingest_jobs USING btree (status);

CREATE INDEX idx_ingest_jobs_tenant ON public.ingest_jobs USING btree (tenant_id);

CREATE INDEX idx_knowledge_brand ON public.knowledge_items USING btree (brand_id);

CREATE INDEX idx_knowledge_category ON public.knowledge_items USING btree (category);

CREATE INDEX idx_knowledge_items_tenant ON public.knowledge_items USING btree (tenant_id);

CREATE INDEX idx_knowledge_source ON public.knowledge_items USING btree (source);

CREATE INDEX idx_notif_brand ON public.notification_log USING btree (brand_id);

CREATE INDEX idx_notif_channel ON public.notification_log USING btree (channel);

CREATE INDEX idx_notif_created ON public.notification_log USING btree (created_at DESC);

CREATE INDEX idx_notif_dedup ON public.notification_log USING btree (brand_id, content_hash, channel);

CREATE INDEX idx_notif_status ON public.notification_log USING btree (status);

CREATE INDEX idx_notif_type ON public.notification_log USING btree (message_type);

CREATE INDEX idx_ooda_acc_task ON public.ooda_cycles USING btree (acc_task_id);

CREATE INDEX idx_ooda_brand ON public.ooda_cycles USING btree (brand_id);

CREATE INDEX idx_ooda_brand_type ON public.ooda_cycles USING btree (brand_id, cycle_type, started_at DESC);

CREATE INDEX idx_ooda_state_paused ON public.ooda_schedule_state USING btree (is_paused);

CREATE INDEX idx_ooda_status ON public.ooda_cycles USING btree (status);

CREATE INDEX idx_operation_logs_tenant ON public.operation_logs USING btree (tenant_id);

CREATE INDEX idx_oplog_action ON public.operation_logs USING btree (action);

CREATE INDEX idx_oplog_brand ON public.operation_logs USING btree (brand_id);

CREATE INDEX idx_oplog_date ON public.operation_logs USING btree (created_at DESC);

CREATE INDEX idx_oplog_user ON public.operation_logs USING btree (user_id);

CREATE INDEX idx_platform_account_brand ON public.platform_accounts USING btree (brand_id);

CREATE INDEX idx_platform_account_platform ON public.platform_accounts USING btree (platform);

CREATE INDEX idx_platform_accounts_tenant ON public.platform_accounts USING btree (tenant_id);

CREATE INDEX idx_probe_brand_id ON public.probe_results USING btree (brand_id);

CREATE UNIQUE INDEX idx_probe_brand_snapshot ON public.probe_results USING btree (brand_id, snapshot_date);

CREATE INDEX idx_probe_date ON public.probe_results USING btree (probe_date DESC);

CREATE INDEX idx_probe_results_tenant ON public.probe_results USING btree (tenant_id);

CREATE INDEX idx_probe_snapshot ON public.probe_results USING btree (snapshot_date);

CREATE INDEX idx_publish_task_brand ON public.publish_tasks USING btree (brand_id);

CREATE INDEX idx_publish_task_date ON public.publish_tasks USING btree (created_at DESC);

CREATE INDEX idx_publish_task_status ON public.publish_tasks USING btree (status);

CREATE INDEX idx_publish_tasks_tenant ON public.publish_tasks USING btree (tenant_id);

CREATE INDEX idx_published_brand ON public.published_contents USING btree (brand_id);

CREATE INDEX idx_published_contents_tenant ON public.published_contents USING btree (tenant_id);

CREATE INDEX idx_published_date ON public.published_contents USING btree (published_at DESC);

CREATE INDEX idx_published_platform ON public.published_contents USING btree (platform);

CREATE INDEX idx_report_brand ON public.reports USING btree (brand_id);

CREATE INDEX idx_report_type ON public.reports USING btree (type);

CREATE INDEX idx_reports_tenant ON public.reports USING btree (tenant_id);

CREATE INDEX idx_strategy_goal_brand ON public.strategy_goals USING btree (brand_id);

CREATE INDEX idx_strategy_goal_deadline ON public.strategy_goals USING btree (deadline);

CREATE INDEX idx_strategy_goal_status ON public.strategy_goals USING btree (status);

CREATE INDEX idx_strategy_plan_brand ON public.strategy_plans USING btree (brand_id);

CREATE INDEX idx_strategy_plan_goal ON public.strategy_plans USING btree (goal_id);

CREATE INDEX idx_strategy_plan_status ON public.strategy_plans USING btree (status);

CREATE INDEX idx_strategy_plan_type ON public.strategy_plans USING btree (plan_type);

CREATE INDEX idx_subscriptions_tenant ON public.subscriptions USING btree (tenant_id);

CREATE INDEX idx_user_casdoor ON public.users USING btree (casdoor_id);

CREATE INDEX idx_users_tenant ON public.users USING btree (tenant_id);

CREATE INDEX idx_weekly_plan_brand ON public.weekly_plans USING btree (brand_id);

CREATE INDEX idx_weekly_plan_status ON public.weekly_plans USING btree (status);

CREATE INDEX idx_weekly_plan_week ON public.weekly_plans USING btree (week_start DESC);

CREATE TRIGGER brands_updated_at BEFORE UPDATE ON public.brands FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER channel_weights_updated_at BEFORE UPDATE ON public.channel_weights FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER crm_leads_updated_at BEFORE UPDATE ON public.crm_leads FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER notification_log_updated_at BEFORE UPDATE ON public.notification_log FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER notification_prefs_updated_at BEFORE UPDATE ON public.notification_preferences FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER ooda_schedule_state_updated_at BEFORE UPDATE ON public.ooda_schedule_state FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER platform_account_updated_at BEFORE UPDATE ON public.platform_accounts FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER published_updated_at BEFORE UPDATE ON public.published_contents FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER strategy_goals_updated_at BEFORE UPDATE ON public.strategy_goals FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER strategy_plans_updated_at BEFORE UPDATE ON public.strategy_plans FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER trg_ingest_jobs_updated BEFORE UPDATE ON public.ingest_jobs FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER trg_knowledge_items_updated BEFORE UPDATE ON public.knowledge_items FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER users_updated_at BEFORE UPDATE ON public.users FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

CREATE TRIGGER weekly_plans_updated_at BEFORE UPDATE ON public.weekly_plans FOR EACH ROW EXECUTE FUNCTION public.update_updated_at();

ALTER TABLE public.alerts ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.brands ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.content_jobs ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.geo_reports ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.ingest_jobs ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.knowledge_items ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.operation_logs ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.platform_accounts ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.probe_results ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.publish_tasks ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.published_contents ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.reports ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.subscriptions ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_alerts ON public.alerts USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_brands ON public.brands USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_content_jobs ON public.content_jobs USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_geo_reports ON public.geo_reports USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_ingest_jobs ON public.ingest_jobs USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_knowledge_items ON public.knowledge_items USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_operation_logs ON public.operation_logs USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_platform_accounts ON public.platform_accounts USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_probe_results ON public.probe_results USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_publish_tasks ON public.publish_tasks USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_published_contents ON public.published_contents USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_reports ON public.reports USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_subscriptions ON public.subscriptions USING (((tenant_id)::text = (public.get_current_tenant())::text));

CREATE POLICY tenant_isolation_users ON public.users USING (((tenant_id)::text = (public.get_current_tenant())::text));

ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
