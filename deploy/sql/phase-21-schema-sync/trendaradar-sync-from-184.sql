-- ============================================================
-- Sync SQL for database: trendaradar
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 43
-- ============================================================

\connect trendaradar

CREATE TABLE public.agent_actions (
    id integer NOT NULL,
    action_id text NOT NULL,
    run_id text,
    rule_id text,
    agent_action text NOT NULL,
    approval_status text DEFAULT 'pending_approval'::text,
    evidence jsonb DEFAULT '{}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.audit_events (
    id integer NOT NULL,
    audit_id text NOT NULL,
    event_id text,
    rule_id text,
    task_id text,
    actor_sub text,
    action text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.brand_collection_jobs (
    id integer NOT NULL,
    brand_id text,
    brand_name text NOT NULL,
    industry text,
    job_type text DEFAULT 'insights'::text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    trigger text DEFAULT 'api'::text NOT NULL,
    requested_by text,
    query_params jsonb DEFAULT '{}'::jsonb,
    result jsonb DEFAULT '{}'::jsonb,
    metrics jsonb DEFAULT '{}'::jsonb,
    error text,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.channel_client_devices (
    id integer NOT NULL,
    device_id text NOT NULL,
    client_name text DEFAULT ''::text NOT NULL,
    region text DEFAULT 'unknown'::text NOT NULL,
    account_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    platforms jsonb DEFAULT '[]'::jsonb NOT NULL,
    load integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'offline'::text NOT NULL,
    last_heartbeat timestamp without time zone,
    registered_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.channel_post_comments (
    id integer NOT NULL,
    run_id text DEFAULT ''::text NOT NULL,
    platform text NOT NULL,
    source_post_id text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    comment_id text NOT NULL,
    parent_id text DEFAULT ''::text NOT NULL,
    author text DEFAULT ''::text NOT NULL,
    author_id text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    like_count integer DEFAULT 0 NOT NULL,
    reply_count integer DEFAULT 0 NOT NULL,
    is_from_target boolean DEFAULT false NOT NULL,
    raw jsonb DEFAULT '{}'::jsonb NOT NULL,
    posted_at timestamp without time zone,
    fetched_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    processed_at timestamp without time zone
);

CREATE TABLE public.channel_post_replies (
    id integer NOT NULL,
    run_id text DEFAULT ''::text NOT NULL,
    platform text NOT NULL,
    source_post_id text DEFAULT ''::text NOT NULL,
    source_comment_id text NOT NULL,
    reply_comment_id text DEFAULT ''::text NOT NULL,
    reply_content text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    posted_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.channel_reply_feedback (
    id integer NOT NULL,
    reply_id text NOT NULL,
    platform text NOT NULL,
    source_post_id text DEFAULT ''::text NOT NULL,
    source_comment_id text DEFAULT ''::text NOT NULL,
    qa_id integer,
    intent text,
    reply_text text NOT NULL,
    re_replied boolean DEFAULT false NOT NULL,
    intent_resolved boolean DEFAULT false NOT NULL,
    reaction_score numeric(5,4),
    captured_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    liked boolean,
    no_complaint boolean DEFAULT true NOT NULL
);

CREATE TABLE public.channel_task_runs (
    id integer NOT NULL,
    run_id text NOT NULL,
    plan_task_id text DEFAULT ''::text NOT NULL,
    template_id text NOT NULL,
    channel text NOT NULL,
    target_type text DEFAULT 'unknown'::text NOT NULL,
    target_ref jsonb DEFAULT '{}'::jsonb NOT NULL,
    properties jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    trigger_type text DEFAULT 'manual'::text NOT NULL,
    requested_by text DEFAULT ''::text NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    metrics jsonb DEFAULT '{}'::jsonb NOT NULL,
    artifacts jsonb DEFAULT '[]'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp without time zone,
    completed_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.collection_rules (
    id integer NOT NULL,
    rule_id text NOT NULL,
    name text NOT NULL,
    lifecycle_status text DEFAULT 'draft'::text,
    source_profile jsonb DEFAULT '{}'::jsonb,
    geo_policy jsonb DEFAULT '{}'::jsonb,
    skill_pipeline jsonb DEFAULT '[]'::jsonb,
    schedule jsonb DEFAULT '{}'::jsonb,
    destination jsonb DEFAULT '{}'::jsonb,
    governance jsonb DEFAULT '{}'::jsonb,
    created_by text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    deleted_at timestamp without time zone
);

CREATE TABLE public.crawl_records (
    id integer NOT NULL,
    url text NOT NULL,
    title text,
    content text,
    source_name text,
    source_type text,
    keywords_matched text[],
    relevance_score double precision DEFAULT 0.0,
    crawled_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    metadata jsonb DEFAULT '{}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.deployment_configs (
    id integer NOT NULL,
    user_id character varying(200) NOT NULL,
    item_id character varying(200) NOT NULL,
    config jsonb DEFAULT '{}'::jsonb,
    status character varying(50) DEFAULT 'pending'::character varying,
    deployed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.geo_shards (
    id integer NOT NULL,
    shard_id text NOT NULL,
    region_set text[] DEFAULT '{}'::text[],
    locale_priority jsonb DEFAULT '[]'::jsonb,
    timezone_window jsonb DEFAULT '{}'::jsonb,
    coverage_score double precision DEFAULT 0.0,
    sla_status text DEFAULT 'ok'::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.global_configs (
    key text NOT NULL,
    value jsonb NOT NULL,
    description text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.intelligence_assets (
    id integer NOT NULL,
    owner_id text NOT NULL,
    title text NOT NULL,
    content text,
    keywords text[],
    source_url text,
    source_type text DEFAULT 'public'::text,
    is_public boolean DEFAULT false,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.intelligence_audit_log (
    id integer NOT NULL,
    asset_id integer,
    action text NOT NULL,
    actor_id text NOT NULL,
    target_user_id text,
    details jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.intelligence_delegations (
    id integer NOT NULL,
    asset_id integer NOT NULL,
    delegate_user_id text NOT NULL,
    delegator text NOT NULL,
    expires_at timestamp without time zone NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.intelligence_shares (
    id integer NOT NULL,
    asset_id integer NOT NULL,
    grantee_user_id text NOT NULL,
    granted_by text NOT NULL,
    granted_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.invoice_exports (
    id integer NOT NULL,
    export_id text NOT NULL,
    mailbox_profile_id text NOT NULL,
    run_id text DEFAULT ''::text NOT NULL,
    export_format text DEFAULT 'xlsx'::text NOT NULL,
    storage_ref text DEFAULT ''::text NOT NULL,
    invoice_count integer DEFAULT 0 NOT NULL,
    duplicate_count integer DEFAULT 0 NOT NULL,
    failed_count integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'created'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.invoice_records (
    id integer NOT NULL,
    invoice_record_id text NOT NULL,
    parsed_document_id text NOT NULL,
    mail_item_id text NOT NULL,
    resource_id text NOT NULL,
    invoice_no text DEFAULT ''::text NOT NULL,
    invoice_code text DEFAULT ''::text NOT NULL,
    invoice_type text DEFAULT 'unknown'::text NOT NULL,
    issue_date date,
    seller_name text DEFAULT ''::text NOT NULL,
    seller_tax_id text DEFAULT ''::text NOT NULL,
    buyer_name text DEFAULT ''::text NOT NULL,
    buyer_tax_id text DEFAULT ''::text NOT NULL,
    amount_without_tax numeric(18,2),
    tax_amount numeric(18,2),
    total_amount numeric(18,2),
    currency text DEFAULT 'CNY'::text NOT NULL,
    source_mail_subject text DEFAULT ''::text NOT NULL,
    source_sender_domain text DEFAULT ''::text NOT NULL,
    source_received_at timestamp without time zone,
    attachment_filename text DEFAULT ''::text NOT NULL,
    attachment_sha256 text DEFAULT ''::text NOT NULL,
    dedupe_key text DEFAULT ''::text NOT NULL,
    parse_status text DEFAULT 'parsed'::text NOT NULL,
    parse_confidence double precision DEFAULT 0.0 NOT NULL,
    review_status text DEFAULT 'pending'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.kb_qa (
    id integer NOT NULL,
    platform text DEFAULT 'all'::text NOT NULL,
    question text NOT NULL,
    answer text NOT NULL,
    intent text DEFAULT 'unknown'::text NOT NULL,
    score numeric(5,4) DEFAULT 0.5 NOT NULL,
    hit_count integer DEFAULT 0 NOT NULL,
    miss_count integer DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    tenant_id text DEFAULT ''::text NOT NULL
);

CREATE TABLE public.keyword_tracking (
    id integer NOT NULL,
    keyword text NOT NULL,
    search_count integer DEFAULT 0,
    last_searched_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.mail_cursors (
    mailbox_profile_id text NOT NULL,
    folder text DEFAULT 'INBOX'::text NOT NULL,
    last_uid text,
    last_seen_at timestamp without time zone,
    checkpoint_status text DEFAULT 'ok'::text NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.mail_ingestion_runs (
    id integer NOT NULL,
    run_id text NOT NULL,
    mailbox_profile_id text NOT NULL,
    trigger_type text DEFAULT 'scheduled'::text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    started_at timestamp without time zone,
    finished_at timestamp without time zone,
    fetched_count integer DEFAULT 0 NOT NULL,
    valid_count integer DEFAULT 0 NOT NULL,
    classified_count integer DEFAULT 0 NOT NULL,
    resource_count integer DEFAULT 0 NOT NULL,
    downloaded_attachment_count integer DEFAULT 0 NOT NULL,
    parsed_document_count integer DEFAULT 0 NOT NULL,
    invoice_count integer DEFAULT 0 NOT NULL,
    export_count integer DEFAULT 0 NOT NULL,
    error_summary text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.mail_items (
    id integer NOT NULL,
    mail_item_id text NOT NULL,
    mailbox_profile_id text NOT NULL,
    folder text DEFAULT 'INBOX'::text NOT NULL,
    uid text DEFAULT ''::text NOT NULL,
    message_id text DEFAULT ''::text NOT NULL,
    thread_key text DEFAULT ''::text NOT NULL,
    sender text DEFAULT ''::text NOT NULL,
    sender_domain text DEFAULT ''::text NOT NULL,
    recipients_masked text[] DEFAULT '{}'::text[] NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    received_at timestamp without time zone,
    body_preview text DEFAULT ''::text NOT NULL,
    body_hash text DEFAULT ''::text NOT NULL,
    validity text DEFAULT 'valid'::text NOT NULL,
    category text DEFAULT 'unknown'::text NOT NULL,
    priority text DEFAULT 'medium'::text NOT NULL,
    classification_confidence double precision DEFAULT 0.0 NOT NULL,
    recommended_actions text[] DEFAULT '{}'::text[] NOT NULL,
    processing_status text DEFAULT 'indexed'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.mail_resources (
    id integer NOT NULL,
    resource_id text NOT NULL,
    mail_item_id text NOT NULL,
    resource_type text DEFAULT 'attachment'::text NOT NULL,
    filename text DEFAULT ''::text NOT NULL,
    mime_type text DEFAULT ''::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    part_id text DEFAULT ''::text NOT NULL,
    sha256 text DEFAULT ''::text NOT NULL,
    storage_ref text DEFAULT ''::text NOT NULL,
    download_status text DEFAULT 'metadata_only'::text NOT NULL,
    policy_reason text DEFAULT ''::text NOT NULL,
    parse_status text DEFAULT 'pending'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.mailbox_profiles (
    id integer NOT NULL,
    profile_id text NOT NULL,
    tenant_id text DEFAULT ''::text NOT NULL,
    owner_user_id text DEFAULT ''::text NOT NULL,
    profile_type text DEFAULT 'shared'::text NOT NULL,
    provider text DEFAULT 'custom'::text NOT NULL,
    address_masked text DEFAULT ''::text NOT NULL,
    imap_host text DEFAULT ''::text NOT NULL,
    imap_port integer DEFAULT 993 NOT NULL,
    imap_ssl boolean DEFAULT true NOT NULL,
    default_folder text DEFAULT 'INBOX'::text NOT NULL,
    secret_ref text DEFAULT ''::text NOT NULL,
    fetch_policy jsonb DEFAULT '{}'::jsonb NOT NULL,
    classification_policy jsonb DEFAULT '{}'::jsonb NOT NULL,
    attachment_policy jsonb DEFAULT '{}'::jsonb NOT NULL,
    invoice_policy jsonb DEFAULT '{}'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    deleted_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.market_categories (
    id integer NOT NULL,
    name character varying(100) NOT NULL,
    slug character varying(100),
    description text,
    icon character varying(200),
    sort_order integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.market_items (
    id integer NOT NULL,
    item_id character varying(200) NOT NULL,
    item_type character varying(50) DEFAULT 'agent'::character varying NOT NULL,
    title character varying(500) NOT NULL,
    description text,
    long_description text,
    author character varying(200),
    avatar_url character varying(500),
    source_id integer,
    source_url character varying(500),
    source_type character varying(50),
    category_id integer,
    tags jsonb DEFAULT '[]'::jsonb,
    stars integer DEFAULT 0,
    downloads integer DEFAULT 0,
    forks integer DEFAULT 0,
    metadata jsonb DEFAULT '{}'::jsonb,
    compatibility jsonb DEFAULT '{}'::jsonb,
    deployment_config jsonb DEFAULT '{}'::jsonb,
    status character varying(20) DEFAULT 'active'::character varying,
    quality_score double precision DEFAULT 0.0,
    popularity_score double precision DEFAULT 0.0,
    last_updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.market_rankings (
    id integer NOT NULL,
    item_id character varying(200) NOT NULL,
    ranking_type character varying(50) DEFAULT 'weekly'::character varying NOT NULL,
    rank integer NOT NULL,
    score double precision DEFAULT 0.0,
    period_start date,
    period_end date,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.market_sources (
    id integer NOT NULL,
    name character varying(100) NOT NULL,
    source_type character varying(50) NOT NULL,
    base_url character varying(500),
    api_url character varying(500),
    enabled boolean DEFAULT true,
    sync_interval_hours integer DEFAULT 24,
    last_synced_at timestamp with time zone,
    config jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.media_accounts (
    account_id text NOT NULL,
    platform text NOT NULL,
    phone text DEFAULT ''::text NOT NULL,
    password text DEFAULT ''::text NOT NULL,
    cookie_raw text DEFAULT ''::text NOT NULL,
    cookie_file_path text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    expires_at timestamp without time zone,
    weibo_uid text DEFAULT ''::text NOT NULL,
    douban_uid text DEFAULT ''::text NOT NULL,
    xhs_uid text DEFAULT ''::text NOT NULL
);

CREATE TABLE public.parsed_documents (
    id integer NOT NULL,
    parsed_document_id text NOT NULL,
    resource_id text NOT NULL,
    document_type text DEFAULT 'unknown'::text NOT NULL,
    source_format text DEFAULT 'unknown'::text NOT NULL,
    extracted_text_ref text DEFAULT ''::text NOT NULL,
    tables_ref text DEFAULT ''::text NOT NULL,
    structured_fields jsonb DEFAULT '{}'::jsonb NOT NULL,
    confidence double precision DEFAULT 0.0 NOT NULL,
    status text DEFAULT 'parsed'::text NOT NULL,
    error_message text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.push_records (
    id integer NOT NULL,
    user_id text NOT NULL,
    channel text NOT NULL,
    title text,
    content text,
    status text DEFAULT 'pending'::text,
    sent_at timestamp without time zone,
    error text,
    metadata jsonb DEFAULT '{}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.rule_runs (
    id integer NOT NULL,
    run_id text NOT NULL,
    rule_id text NOT NULL,
    task_id text,
    status text DEFAULT 'queued'::text,
    started_at timestamp without time zone,
    completed_at timestamp without time zone,
    metrics jsonb DEFAULT '{}'::jsonb,
    error text,
    attribution jsonb DEFAULT '{}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.skill_registry (
    id integer NOT NULL,
    skill_id text NOT NULL,
    version text DEFAULT '1.0.0'::text NOT NULL,
    display_name text NOT NULL,
    category text DEFAULT 'web'::text,
    source_id text,
    input_schema jsonb DEFAULT '{}'::jsonb,
    output_schema jsonb DEFAULT '{}'::jsonb,
    health_status text DEFAULT 'unknown'::text,
    last_checked_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.source_configs (
    id integer NOT NULL,
    source_id text NOT NULL,
    source_name text NOT NULL,
    source_url text NOT NULL,
    source_type text DEFAULT 'news'::text,
    enabled boolean DEFAULT true,
    priority integer DEFAULT 5,
    config jsonb DEFAULT '{}'::jsonb,
    health_status text DEFAULT 'unknown'::text,
    last_checked_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.sync_tasks (
    id integer NOT NULL,
    task_type character varying(100) NOT NULL,
    status character varying(50) DEFAULT 'pending'::character varying,
    source_id integer,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    items_processed integer DEFAULT 0,
    items_added integer DEFAULT 0,
    items_updated integer DEFAULT 0,
    error_message text,
    config jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    task_id text,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.task_records (
    id integer NOT NULL,
    task_id text NOT NULL,
    task_name text NOT NULL,
    task_type text NOT NULL,
    status text DEFAULT 'pending'::text,
    crawler_config jsonb DEFAULT '{}'::jsonb,
    schedule_config jsonb DEFAULT '{}'::jsonb,
    result jsonb,
    error text,
    started_at timestamp without time zone,
    completed_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.token_accounts (
    account_id text NOT NULL,
    platform text NOT NULL,
    token_type text DEFAULT 'cookie'::text NOT NULL,
    cookie_raw text DEFAULT ''::text NOT NULL,
    pat_token text DEFAULT ''::text NOT NULL,
    refresh_token text DEFAULT ''::text NOT NULL,
    oauth_token text DEFAULT ''::text NOT NULL,
    oauth_secret text DEFAULT ''::text NOT NULL,
    expires_at timestamp without time zone,
    refreshed_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.token_meter_events (
    id bigint NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    user_sub text DEFAULT ''::text NOT NULL,
    rule_id text DEFAULT ''::text NOT NULL,
    route text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL
);

CREATE TABLE public.user_collections (
    id integer NOT NULL,
    user_id character varying(200) NOT NULL,
    item_id character varying(200) NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.user_downloads (
    id integer NOT NULL,
    user_id character varying(200) NOT NULL,
    item_id character varying(200) NOT NULL,
    version character varying(100),
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.user_push_configs (
    id integer NOT NULL,
    user_id text NOT NULL,
    channel text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enabled boolean DEFAULT true,
    notification_keywords text[] DEFAULT '{}'::text[],
    notification_sources text[] DEFAULT '{}'::text[],
    notification_schedule jsonb DEFAULT '{"type": "realtime"}'::jsonb,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_agent_actions_action_id ON public.agent_actions USING btree (action_id);

CREATE INDEX idx_agent_actions_approval ON public.agent_actions USING btree (approval_status);

CREATE UNIQUE INDEX idx_audit_events_audit_id ON public.audit_events USING btree (audit_id);

CREATE INDEX idx_audit_events_created_at ON public.audit_events USING btree (created_at);

CREATE INDEX idx_audit_events_rule_id ON public.audit_events USING btree (rule_id);

CREATE INDEX idx_brand_jobs_brand_id ON public.brand_collection_jobs USING btree (brand_id);

CREATE INDEX idx_brand_jobs_brand_name ON public.brand_collection_jobs USING btree (brand_name);

CREATE INDEX idx_brand_jobs_created_at ON public.brand_collection_jobs USING btree (created_at DESC);

CREATE INDEX idx_brand_jobs_status ON public.brand_collection_jobs USING btree (status);

CREATE INDEX idx_brand_jobs_type_status ON public.brand_collection_jobs USING btree (job_type, status);

CREATE INDEX idx_channel_client_devices_region ON public.channel_client_devices USING btree (region) WHERE (status = 'online'::text);

CREATE INDEX idx_channel_client_devices_status ON public.channel_client_devices USING btree (status, last_heartbeat DESC);

CREATE INDEX idx_channel_task_runs_channel ON public.channel_task_runs USING btree (channel, created_at DESC);

CREATE INDEX idx_channel_task_runs_plan ON public.channel_task_runs USING btree (plan_task_id, created_at DESC);

CREATE INDEX idx_channel_task_runs_status ON public.channel_task_runs USING btree (status, created_at DESC);

CREATE INDEX idx_channel_task_runs_template ON public.channel_task_runs USING btree (template_id, created_at DESC);

CREATE INDEX idx_collection_rules_deleted_at ON public.collection_rules USING btree (deleted_at);

CREATE INDEX idx_collection_rules_lifecycle ON public.collection_rules USING btree (lifecycle_status);

CREATE INDEX idx_cpc_post_time ON public.channel_post_comments USING btree (platform, source_post_id, posted_at DESC);

CREATE INDEX idx_cpc_target ON public.channel_post_comments USING btree (platform, is_from_target) WHERE (is_from_target = true);

CREATE INDEX idx_cpc_unprocessed ON public.channel_post_comments USING btree (processed_at) WHERE (processed_at IS NULL);

CREATE INDEX idx_cpr_post ON public.channel_post_replies USING btree (platform, source_post_id, created_at DESC);

CREATE INDEX idx_cpr_status ON public.channel_post_replies USING btree (status, created_at DESC);

CREATE INDEX idx_crawl_records_crawled_at ON public.crawl_records USING btree (crawled_at);

CREATE INDEX idx_crawl_records_keywords ON public.crawl_records USING gin (keywords_matched);

CREATE INDEX idx_crawl_records_source ON public.crawl_records USING btree (source_name);

CREATE INDEX idx_crawl_records_url ON public.crawl_records USING btree (url);

CREATE INDEX idx_feedback_capture ON public.channel_reply_feedback USING btree (platform, captured_at DESC);

CREATE INDEX idx_feedback_qa ON public.channel_reply_feedback USING btree (qa_id, captured_at DESC);

CREATE INDEX idx_intelligence_assets_created ON public.intelligence_assets USING btree (created_at);

CREATE INDEX idx_intelligence_assets_owner ON public.intelligence_assets USING btree (owner_id);

CREATE INDEX idx_intelligence_assets_public ON public.intelligence_assets USING btree (is_public);

CREATE INDEX idx_intelligence_audit_asset ON public.intelligence_audit_log USING btree (asset_id);

CREATE INDEX idx_intelligence_delegations_asset ON public.intelligence_delegations USING btree (asset_id);

CREATE INDEX idx_intelligence_delegations_delegate ON public.intelligence_delegations USING btree (delegate_user_id);

CREATE INDEX idx_intelligence_delegations_expires ON public.intelligence_delegations USING btree (expires_at);

CREATE INDEX idx_intelligence_shares_asset ON public.intelligence_shares USING btree (asset_id);

CREATE INDEX idx_intelligence_shares_grantee ON public.intelligence_shares USING btree (grantee_user_id);

CREATE INDEX idx_invoice_exports_profile ON public.invoice_exports USING btree (mailbox_profile_id, created_at DESC);

CREATE INDEX idx_invoice_records_dedupe ON public.invoice_records USING btree (dedupe_key);

CREATE INDEX idx_invoice_records_no_code ON public.invoice_records USING btree (invoice_code, invoice_no);

CREATE INDEX idx_invoice_records_status ON public.invoice_records USING btree (parse_status, review_status, issue_date DESC);

CREATE INDEX idx_kb_intent ON public.kb_qa USING btree (platform, intent) WHERE (enabled = true);

CREATE INDEX idx_kb_question_trgm ON public.kb_qa USING gin (question public.gin_trgm_ops);

CREATE INDEX idx_kb_tenant ON public.kb_qa USING btree (tenant_id) WHERE (enabled = true);

CREATE INDEX idx_keyword_tracking_keyword ON public.keyword_tracking USING btree (keyword);

CREATE INDEX idx_mail_items_category ON public.mail_items USING btree (category, priority, received_at DESC);

CREATE INDEX idx_mail_items_message ON public.mail_items USING btree (mailbox_profile_id, message_id);

CREATE UNIQUE INDEX idx_mail_items_profile_folder_uid ON public.mail_items USING btree (mailbox_profile_id, folder, uid) WHERE (uid <> ''::text);

CREATE INDEX idx_mail_items_profile_received ON public.mail_items USING btree (mailbox_profile_id, received_at DESC);

CREATE INDEX idx_mail_resources_mail_item ON public.mail_resources USING btree (mail_item_id);

CREATE INDEX idx_mail_resources_sha256 ON public.mail_resources USING btree (sha256);

CREATE INDEX idx_mail_resources_status ON public.mail_resources USING btree (download_status, parse_status);

CREATE INDEX idx_mail_runs_created ON public.mail_ingestion_runs USING btree (created_at DESC);

CREATE INDEX idx_mail_runs_profile ON public.mail_ingestion_runs USING btree (mailbox_profile_id);

CREATE INDEX idx_mail_runs_status ON public.mail_ingestion_runs USING btree (status);

CREATE INDEX idx_mailbox_profiles_enabled ON public.mailbox_profiles USING btree (enabled) WHERE (deleted_at IS NULL);

CREATE INDEX idx_mailbox_profiles_owner ON public.mailbox_profiles USING btree (owner_user_id);

CREATE INDEX idx_mailbox_profiles_tenant ON public.mailbox_profiles USING btree (tenant_id);

CREATE INDEX idx_media_accounts_douban_uid ON public.media_accounts USING btree (douban_uid) WHERE (douban_uid <> ''::text);

CREATE INDEX idx_media_accounts_phone ON public.media_accounts USING btree (phone);

CREATE INDEX idx_media_accounts_platform ON public.media_accounts USING btree (platform, status);

CREATE INDEX idx_media_accounts_weibo_uid ON public.media_accounts USING btree (weibo_uid) WHERE (weibo_uid <> ''::text);

CREATE INDEX idx_media_accounts_xhs_uid ON public.media_accounts USING btree (xhs_uid) WHERE (xhs_uid <> ''::text);

CREATE INDEX idx_parsed_documents_resource ON public.parsed_documents USING btree (resource_id);

CREATE INDEX idx_parsed_documents_type ON public.parsed_documents USING btree (document_type, status);

CREATE INDEX idx_push_records_created_at ON public.push_records USING btree (created_at);

CREATE INDEX idx_push_records_status ON public.push_records USING btree (status);

CREATE INDEX idx_push_records_user_id ON public.push_records USING btree (user_id);

CREATE INDEX idx_rule_runs_created_at ON public.rule_runs USING btree (created_at);

CREATE INDEX idx_rule_runs_rule_id ON public.rule_runs USING btree (rule_id);

CREATE UNIQUE INDEX idx_rule_runs_run_id ON public.rule_runs USING btree (run_id);

CREATE INDEX idx_rule_runs_status ON public.rule_runs USING btree (status);

CREATE INDEX idx_skill_registry_skill_id ON public.skill_registry USING btree (skill_id);

CREATE INDEX idx_source_configs_enabled ON public.source_configs USING btree (enabled);

CREATE INDEX idx_source_configs_source_id ON public.source_configs USING btree (source_id);

CREATE INDEX idx_sync_tasks_status_created_at ON public.sync_tasks USING btree (status, created_at DESC);

CREATE UNIQUE INDEX idx_sync_tasks_task_id ON public.sync_tasks USING btree (task_id);

CREATE INDEX idx_task_records_created_at ON public.task_records USING btree (created_at);

CREATE INDEX idx_task_records_status ON public.task_records USING btree (status);

CREATE INDEX idx_task_records_task_id ON public.task_records USING btree (task_id);

CREATE INDEX idx_token_expires ON public.token_accounts USING btree (expires_at) WHERE (expires_at IS NOT NULL);

CREATE INDEX idx_token_meter_occurred_at ON public.token_meter_events USING btree (occurred_at DESC);

CREATE INDEX idx_token_meter_rule_id ON public.token_meter_events USING btree (rule_id);

CREATE INDEX idx_token_meter_user_sub ON public.token_meter_events USING btree (user_sub);

CREATE INDEX idx_token_platform ON public.token_accounts USING btree (platform, status);

CREATE INDEX idx_user_push_configs_channel ON public.user_push_configs USING btree (channel);

CREATE INDEX idx_user_push_configs_enabled ON public.user_push_configs USING btree (enabled);

CREATE INDEX idx_user_push_configs_user_id ON public.user_push_configs USING btree (user_id);
