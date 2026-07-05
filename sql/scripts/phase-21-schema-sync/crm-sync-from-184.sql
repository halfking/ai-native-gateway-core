-- ============================================================
-- Sync SQL for database: crm
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 24
-- ============================================================

\connect crm

CREATE TABLE public.crm_activities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    customer_id uuid NOT NULL,
    opportunity_id uuid,
    activity_type character varying(20) NOT NULL,
    summary text NOT NULL,
    outcome text,
    scheduled_at timestamp with time zone,
    owner_id character varying(100),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_activities_activity_type_check CHECK (((activity_type)::text = ANY (ARRAY[('call'::character varying)::text, ('visit'::character varying)::text, ('email'::character varying)::text, ('meeting'::character varying)::text, ('note'::character varying)::text])))
);

CREATE TABLE public.crm_ai_replies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    conversation_id uuid NOT NULL,
    trigger_message_id uuid,
    status character varying(24) DEFAULT 'pending_review'::character varying NOT NULL,
    intent character varying(60),
    intent_payload jsonb,
    draft_content text NOT NULL,
    final_content text,
    rag_sources jsonb,
    model_l1 character varying(80),
    model_l2 character varying(80),
    cache_hit boolean DEFAULT false NOT NULL,
    cache_id uuid,
    confidence numeric(4,3),
    review_by character varying(100),
    reviewed_at timestamp with time zone,
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_ai_replies_status_check CHECK (((status)::text = ANY (ARRAY[('pending_review'::character varying)::text, ('approved'::character varying)::text, ('sent'::character varying)::text, ('rejected'::character varying)::text, ('superseded'::character varying)::text, ('auto_send'::character varying)::text])))
);

CREATE TABLE public.crm_ai_routing_policy (
    id smallint DEFAULT 1 NOT NULL,
    l1_model character varying(80) DEFAULT 'deepseek-chat'::character varying NOT NULL,
    l2_model character varying(80) DEFAULT 'deepseek-chat'::character varying NOT NULL,
    embedding_model character varying(80) DEFAULT 'text-embedding-3-small'::character varying NOT NULL,
    cache_threshold numeric(4,3) DEFAULT 0.95 NOT NULL,
    rag_top_k integer DEFAULT 3 NOT NULL,
    max_context_chars integer DEFAULT 4000 NOT NULL,
    auto_send_for_tiers jsonb DEFAULT '[]'::jsonb NOT NULL,
    daily_token_budget integer DEFAULT 2000000 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    daily_cost_budget numeric(10,2) DEFAULT 50.00 NOT NULL,
    CONSTRAINT crm_ai_routing_policy_id_check CHECK ((id = 1))
);

CREATE TABLE public.crm_alerts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    severity character varying(20) DEFAULT 'warn'::character varying NOT NULL,
    category character varying(40) NOT NULL,
    title character varying(255) NOT NULL,
    detail text,
    source character varying(80),
    channel_id uuid,
    status character varying(20) DEFAULT 'open'::character varying NOT NULL,
    acked_by character varying(100),
    acked_at timestamp with time zone,
    resolved_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_alerts_severity_check CHECK (((severity)::text = ANY (ARRAY[('info'::character varying)::text, ('warn'::character varying)::text, ('error'::character varying)::text, ('critical'::character varying)::text]))),
    CONSTRAINT crm_alerts_status_check CHECK (((status)::text = ANY (ARRAY[('open'::character varying)::text, ('ack'::character varying)::text, ('resolved'::character varying)::text, ('suppressed'::character varying)::text])))
);

CREATE TABLE public.crm_channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    platform character varying(40) DEFAULT 'douyin_feige'::character varying NOT NULL,
    account_name character varying(200) NOT NULL,
    shop_id character varying(100),
    cdp_endpoint character varying(255),
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    last_seen_at timestamp with time zone,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    channel_kind character varying(20) DEFAULT 'rpa'::character varying NOT NULL,
    external_account_id character varying(160),
    CONSTRAINT crm_channels_channel_kind_check CHECK (((channel_kind)::text = ANY (ARRAY[('rpa'::character varying)::text, ('openapi'::character varying)::text]))),
    CONSTRAINT crm_channels_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('paused'::character varying)::text, ('offline'::character varying)::text, ('disabled'::character varying)::text])))
);

CREATE TABLE public.crm_conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    channel_id uuid NOT NULL,
    customer_id uuid,
    visitor_uid character varying(120) NOT NULL,
    visitor_name character varying(200),
    visitor_avatar character varying(500),
    status character varying(20) DEFAULT 'open'::character varying NOT NULL,
    priority smallint DEFAULT 3 NOT NULL,
    intent character varying(60),
    intent_score numeric(4,3),
    last_message_at timestamp with time zone,
    last_message_preview text,
    unread_count integer DEFAULT 0 NOT NULL,
    assigned_to character varying(100),
    auto_reply boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_conversations_priority_check CHECK (((priority >= 1) AND (priority <= 5))),
    CONSTRAINT crm_conversations_status_check CHECK (((status)::text = ANY (ARRAY[('open'::character varying)::text, ('waiting'::character varying)::text, ('escalated'::character varying)::text, ('resolved'::character varying)::text, ('archived'::character varying)::text])))
);

CREATE TABLE public.crm_customers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    phone character varying(50),
    email character varying(200),
    company character varying(200),
    address text,
    notes text,
    source character varying(50) DEFAULT 'manual'::character varying,
    status character varying(20) DEFAULT 'active'::character varying,
    owner_id character varying(100),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    douyin_uid character varying(120),
    channel_id uuid,
    tier character varying(20) DEFAULT 'normal'::character varying,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    region character varying(80),
    intent_score numeric(4,3),
    lifetime_value numeric(15,2),
    conversion_probability numeric(4,3),
    expected_revenue numeric(15,2),
    lead_score integer,
    lead_grade character varying(1),
    lead_score_reason text,
    lead_score_updated_at timestamp with time zone,
    CONSTRAINT crm_customers_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('inactive'::character varying)::text, ('blacklist'::character varying)::text]))),
    CONSTRAINT crm_customers_tier_check CHECK (((tier)::text = ANY (ARRAY[('normal'::character varying)::text, ('silver'::character varying)::text, ('gold'::character varying)::text, ('vip'::character varying)::text, ('blacklist'::character varying)::text])))
);

CREATE TABLE public.crm_kb_chunks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    chunk_index integer NOT NULL,
    content text NOT NULL,
    token_count integer,
    embedding jsonb,
    embedding_model character varying(80),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_kb_documents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    title character varying(255) NOT NULL,
    category character varying(80),
    source character varying(40) DEFAULT 'manual'::character varying NOT NULL,
    body text NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    status character varying(20) DEFAULT 'draft'::character varying NOT NULL,
    owner_id character varying(100),
    external_ref character varying(200),
    indexed_at timestamp with time zone,
    impact_level character varying(20) DEFAULT 'medium'::character varying,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_kb_documents_impact_level_check CHECK (((impact_level)::text = ANY (ARRAY[('low'::character varying)::text, ('medium'::character varying)::text, ('high'::character varying)::text, ('critical'::character varying)::text]))),
    CONSTRAINT crm_kb_documents_source_check CHECK (((source)::text = ANY (ARRAY[('manual'::character varying)::text, ('upload'::character varying)::text, ('crawl'::character varying)::text, ('sync'::character varying)::text]))),
    CONSTRAINT crm_kb_documents_status_check CHECK (((status)::text = ANY (ARRAY[('draft'::character varying)::text, ('published'::character varying)::text, ('archived'::character varying)::text])))
);

CREATE TABLE public.crm_kb_gaps (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    topic character varying(255) NOT NULL,
    description text,
    sample_question text,
    conversation_id uuid,
    impact_level character varying(20) DEFAULT 'medium'::character varying,
    status character varying(20) DEFAULT 'open'::character varying NOT NULL,
    owner_id character varying(100),
    eta date,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_kb_gaps_impact_level_check CHECK (((impact_level)::text = ANY (ARRAY[('low'::character varying)::text, ('medium'::character varying)::text, ('high'::character varying)::text, ('critical'::character varying)::text]))),
    CONSTRAINT crm_kb_gaps_status_check CHECK (((status)::text = ANY (ARRAY[('open'::character varying)::text, ('in_progress'::character varying)::text, ('resolved'::character varying)::text, ('wontfix'::character varying)::text])))
);

CREATE TABLE public.crm_lead_scores (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    customer_id uuid NOT NULL,
    conversation_id uuid,
    conversion_probability numeric(4,3) NOT NULL,
    expected_revenue numeric(15,2) DEFAULT 0 NOT NULL,
    lead_score integer NOT NULL,
    lead_grade character varying(1) NOT NULL,
    signals jsonb DEFAULT '[]'::jsonb NOT NULL,
    rationale text,
    model character varying(80) DEFAULT 'rules-v1'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_lead_scores_conversion_probability_check CHECK (((conversion_probability >= (0)::numeric) AND (conversion_probability <= (1)::numeric))),
    CONSTRAINT crm_lead_scores_lead_grade_check CHECK (((lead_grade)::text = ANY (ARRAY[('A'::character varying)::text, ('B'::character varying)::text, ('C'::character varying)::text, ('D'::character varying)::text]))),
    CONSTRAINT crm_lead_scores_lead_score_check CHECK (((lead_score >= 0) AND (lead_score <= 100)))
);

CREATE TABLE public.crm_llm_usage (
    id bigint NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    module character varying(40) NOT NULL,
    conversation_id uuid,
    ai_reply_id uuid,
    model character varying(80) NOT NULL,
    provider character varying(40),
    prompt_tokens integer DEFAULT 0 NOT NULL,
    completion_tokens integer DEFAULT 0 NOT NULL,
    total_tokens integer GENERATED ALWAYS AS ((prompt_tokens + completion_tokens)) STORED,
    cost_rmb numeric(10,6) DEFAULT 0 NOT NULL,
    latency_ms integer,
    cache_hit boolean DEFAULT false NOT NULL,
    request_id character varying(120),
    error text
);

CREATE TABLE public.crm_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    conversation_id uuid NOT NULL,
    direction character varying(10) NOT NULL,
    sender_role character varying(20) NOT NULL,
    sender_id character varying(120),
    content_type character varying(20) DEFAULT 'text'::character varying NOT NULL,
    content text NOT NULL,
    raw_payload jsonb,
    external_id character varying(200),
    ai_reply_id uuid,
    sent_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_messages_content_type_check CHECK (((content_type)::text = ANY (ARRAY[('text'::character varying)::text, ('image'::character varying)::text, ('emoji'::character varying)::text, ('card'::character varying)::text, ('system'::character varying)::text]))),
    CONSTRAINT crm_messages_direction_check CHECK (((direction)::text = ANY (ARRAY[('inbound'::character varying)::text, ('outbound'::character varying)::text, ('system'::character varying)::text]))),
    CONSTRAINT crm_messages_sender_role_check CHECK (((sender_role)::text = ANY (ARRAY[('visitor'::character varying)::text, ('operator'::character varying)::text, ('ai'::character varying)::text, ('system'::character varying)::text])))
);

CREATE TABLE public.crm_oauth_states (
    state character varying(64) NOT NULL,
    meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE public.crm_openapi_events (
    id bigint NOT NULL,
    shop_id character varying(100),
    event_type character varying(80) NOT NULL,
    msg_id character varying(120),
    payload jsonb NOT NULL,
    signature_valid boolean DEFAULT false NOT NULL,
    processed_at timestamp with time zone,
    process_error text,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_openapi_quota (
    id bigint NOT NULL,
    shop_id character varying(100) NOT NULL,
    api_method character varying(120) NOT NULL,
    window_start timestamp with time zone NOT NULL,
    count integer DEFAULT 0 NOT NULL,
    limit_per_min integer DEFAULT 60 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_opportunities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    customer_id uuid NOT NULL,
    title character varying(200) NOT NULL,
    stage character varying(50) DEFAULT 'lead'::character varying,
    amount numeric(15,2),
    expected_close_date date,
    owner_id character varying(100),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_opportunities_stage_check CHECK (((stage)::text = ANY (ARRAY[('lead'::character varying)::text, ('qualified'::character varying)::text, ('proposal'::character varying)::text, ('won'::character varying)::text, ('lost'::character varying)::text])))
);

CREATE TABLE public.crm_outbound_queue (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    conversation_id uuid NOT NULL,
    channel_id uuid NOT NULL,
    message_id uuid,
    ai_reply_id uuid,
    content text NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    locked_by character varying(120),
    locked_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_at timestamp with time zone,
    outbound_meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    next_attempt_at timestamp with time zone,
    CONSTRAINT crm_outbound_queue_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('locked'::character varying)::text, ('sent'::character varying)::text, ('failed'::character varying)::text, ('cancelled'::character varying)::text, ('dead_letter'::character varying)::text])))
);

COMMENT ON COLUMN public.crm_outbound_queue.outbound_meta IS 'Anti-Bot 参数：typingMs, thinkingMs, totalMs, offsetX, offsetY, moveSteps 等，供 RPA Worker 使用';

COMMENT ON COLUMN public.crm_outbound_queue.next_attempt_at IS 'pending 状态下最早允许再次被 dispatcher 拾取的时间';

CREATE TABLE public.crm_prompts (
    key character varying(80) NOT NULL,
    title character varying(200) NOT NULL,
    description text,
    template text NOT NULL,
    variables jsonb DEFAULT '[]'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    updated_by character varying(100),
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_rpa_selectors (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    page_key character varying(80) NOT NULL,
    element_key character varying(80) NOT NULL,
    selector text NOT NULL,
    strategy character varying(20) DEFAULT 'css'::character varying NOT NULL,
    fallback text,
    health character varying(20) DEFAULT 'unknown'::character varying NOT NULL,
    last_used_at timestamp with time zone,
    last_failed_at timestamp with time zone,
    fail_count integer DEFAULT 0 NOT NULL,
    notes text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_rpa_selectors_health_check CHECK (((health)::text = ANY (ARRAY[('healthy'::character varying)::text, ('warn'::character varying)::text, ('broken'::character varying)::text, ('unknown'::character varying)::text]))),
    CONSTRAINT crm_rpa_selectors_strategy_check CHECK (((strategy)::text = ANY (ARRAY[('css'::character varying)::text, ('xpath'::character varying)::text, ('text'::character varying)::text, ('aria'::character varying)::text])))
);

CREATE TABLE public.crm_schema_migrations (
    version character varying(80) NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_semantic_cache (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    question text NOT NULL,
    answer text NOT NULL,
    intent character varying(60),
    embedding jsonb NOT NULL,
    embedding_dim integer NOT NULL,
    embedding_model character varying(80) NOT NULL,
    hit_count integer DEFAULT 0 NOT NULL,
    last_hit_at timestamp with time zone,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_settings (
    key character varying(120) NOT NULL,
    value jsonb NOT NULL,
    description text,
    updated_by character varying(100),
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.crm_shop_authorizations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    shop_id character varying(100) NOT NULL,
    shop_name character varying(200),
    app_key character varying(100) NOT NULL,
    access_token_enc bytea,
    refresh_token_enc bytea,
    scope text[],
    industry character varying(60),
    expires_at timestamp with time zone,
    status character varying(20) DEFAULT 'enabled'::character varying NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    refreshed_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT crm_shop_authorizations_status_check CHECK (((status)::text = ANY (ARRAY[('enabled'::character varying)::text, ('disabled'::character varying)::text, ('expired'::character varying)::text, ('revoked'::character varying)::text])))
);

CREATE INDEX idx_crm_act_customer ON public.crm_activities USING btree (customer_id);

CREATE INDEX idx_crm_act_type ON public.crm_activities USING btree (activity_type);

CREATE INDEX idx_crm_ai_conv ON public.crm_ai_replies USING btree (conversation_id, created_at DESC);

CREATE INDEX idx_crm_ai_status ON public.crm_ai_replies USING btree (status);

CREATE INDEX idx_crm_alerts_severity ON public.crm_alerts USING btree (severity);

CREATE INDEX idx_crm_alerts_status ON public.crm_alerts USING btree (status, created_at DESC);

CREATE INDEX idx_crm_cache_expires ON public.crm_semantic_cache USING btree (expires_at);

CREATE INDEX idx_crm_cache_intent ON public.crm_semantic_cache USING btree (intent);

CREATE INDEX idx_crm_channels_external_account ON public.crm_channels USING btree (platform, channel_kind, external_account_id) WHERE (external_account_id IS NOT NULL);

CREATE INDEX idx_crm_conv_customer ON public.crm_conversations USING btree (customer_id);

CREATE INDEX idx_crm_conv_last_msg ON public.crm_conversations USING btree (last_message_at DESC);

CREATE INDEX idx_crm_conv_priority ON public.crm_conversations USING btree (priority);

CREATE INDEX idx_crm_conv_status ON public.crm_conversations USING btree (status);

CREATE INDEX idx_crm_customers_company ON public.crm_customers USING btree (company);

CREATE INDEX idx_crm_customers_douyin_uid ON public.crm_customers USING btree (douyin_uid);

CREATE INDEX idx_crm_customers_lead_score ON public.crm_customers USING btree (lead_score DESC NULLS LAST);

CREATE INDEX idx_crm_customers_name ON public.crm_customers USING gin (to_tsvector('simple'::regconfig, (name)::text));

CREATE INDEX idx_crm_customers_owner ON public.crm_customers USING btree (owner_id);

CREATE INDEX idx_crm_customers_phone ON public.crm_customers USING btree (phone);

CREATE INDEX idx_crm_customers_tier ON public.crm_customers USING btree (tier);

CREATE INDEX idx_crm_kb_category ON public.crm_kb_documents USING btree (category);

CREATE INDEX idx_crm_kb_gaps_status ON public.crm_kb_gaps USING btree (status);

CREATE INDEX idx_crm_kb_status ON public.crm_kb_documents USING btree (status);

CREATE INDEX idx_crm_lead_scores_customer ON public.crm_lead_scores USING btree (customer_id, created_at DESC);

CREATE INDEX idx_crm_lead_scores_grade ON public.crm_lead_scores USING btree (lead_grade);

CREATE INDEX idx_crm_msg_conv ON public.crm_messages USING btree (conversation_id, sent_at DESC);

CREATE INDEX idx_crm_msg_direction ON public.crm_messages USING btree (direction);

CREATE INDEX idx_crm_oauth_states_expires ON public.crm_oauth_states USING btree (expires_at) WHERE (expires_at IS NOT NULL);

CREATE INDEX idx_crm_openapi_events_received ON public.crm_openapi_events USING btree (received_at DESC);

CREATE INDEX idx_crm_openapi_events_shop ON public.crm_openapi_events USING btree (shop_id, event_type, received_at DESC);

CREATE INDEX idx_crm_opp_customer ON public.crm_opportunities USING btree (customer_id);

CREATE INDEX idx_crm_opp_stage ON public.crm_opportunities USING btree (stage);

CREATE INDEX idx_crm_out_dead_letter ON public.crm_outbound_queue USING btree (status, created_at) WHERE ((status)::text = 'dead_letter'::text);

CREATE INDEX idx_crm_out_pending_next ON public.crm_outbound_queue USING btree (status, channel_id, created_at) WHERE ((status)::text = 'pending'::text);

CREATE INDEX idx_crm_out_status_channel ON public.crm_outbound_queue USING btree (status, channel_id, created_at);

CREATE INDEX idx_crm_shop_auth_expires ON public.crm_shop_authorizations USING btree (expires_at) WHERE ((status)::text = 'enabled'::text);

CREATE INDEX idx_crm_usage_conv ON public.crm_llm_usage USING btree (conversation_id);

CREATE INDEX idx_crm_usage_module ON public.crm_llm_usage USING btree (module);

CREATE INDEX idx_crm_usage_time ON public.crm_llm_usage USING btree (occurred_at DESC);

CREATE UNIQUE INDEX uq_crm_channels_platform_account ON public.crm_channels USING btree (platform, account_name);

CREATE UNIQUE INDEX uq_crm_conv_channel_visitor ON public.crm_conversations USING btree (channel_id, visitor_uid);

CREATE UNIQUE INDEX uq_crm_kb_chunk_doc_idx ON public.crm_kb_chunks USING btree (document_id, chunk_index);

CREATE UNIQUE INDEX uq_crm_msg_external ON public.crm_messages USING btree (conversation_id, external_id) WHERE (external_id IS NOT NULL);

CREATE UNIQUE INDEX uq_crm_openapi_events_msgid ON public.crm_openapi_events USING btree (msg_id) WHERE (msg_id IS NOT NULL);

CREATE UNIQUE INDEX uq_crm_openapi_quota_window ON public.crm_openapi_quota USING btree (shop_id, api_method, window_start);

CREATE UNIQUE INDEX uq_crm_selectors_page_element ON public.crm_rpa_selectors USING btree (page_key, element_key);

CREATE UNIQUE INDEX uq_crm_shop_auth_shop ON public.crm_shop_authorizations USING btree (shop_id);
