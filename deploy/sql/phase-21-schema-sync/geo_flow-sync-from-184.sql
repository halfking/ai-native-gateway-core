-- ============================================================
-- Sync SQL for database: geo_flow
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 48
-- ============================================================

\connect geo_flow

CREATE TABLE public.admin_activity_logs (
    id bigint NOT NULL,
    admin_id bigint,
    admin_username character varying(50) NOT NULL,
    admin_role character varying(20) DEFAULT 'admin'::character varying,
    action character varying(120) NOT NULL,
    request_method character varying(10) DEFAULT 'POST'::character varying,
    page character varying(255) DEFAULT ''::character varying,
    target_type character varying(50) DEFAULT ''::character varying,
    target_id bigint,
    ip_address character varying(64) DEFAULT ''::character varying,
    details text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.admins (
    id bigint NOT NULL,
    username character varying(50) NOT NULL,
    password character varying(255) NOT NULL,
    email character varying(100) DEFAULT ''::character varying NOT NULL,
    display_name character varying(100) DEFAULT ''::character varying NOT NULL,
    role character varying(20) DEFAULT 'admin'::character varying NOT NULL,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_by bigint,
    last_login timestamp(0) without time zone,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    remember_token character varying(100),
    welcome_seen_version character varying(120),
    welcome_dismissed_at timestamp(0) without time zone
);

COMMENT ON COLUMN public.admins.id IS '主键';

COMMENT ON COLUMN public.admins.username IS '登录账号，唯一';

COMMENT ON COLUMN public.admins.password IS 'password_hash 存储';

COMMENT ON COLUMN public.admins.email IS '联系邮箱';

COMMENT ON COLUMN public.admins.display_name IS '展示名称';

COMMENT ON COLUMN public.admins.role IS '角色标识';

COMMENT ON COLUMN public.admins.status IS 'active/disabled 等';

COMMENT ON COLUMN public.admins.created_by IS '创建人管理员 ID';

COMMENT ON COLUMN public.admins.last_login IS '最后登录时间';

COMMENT ON COLUMN public.admins.welcome_seen_version IS '已展示的欢迎/更新弹窗版本键';

COMMENT ON COLUMN public.admins.welcome_dismissed_at IS '用户主动关闭欢迎弹窗时间';

CREATE TABLE public.ai_models (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    version character varying(50) DEFAULT ''::character varying,
    api_key character varying(500) NOT NULL,
    model_id character varying(100) NOT NULL,
    model_type character varying(20) DEFAULT 'chat'::character varying,
    api_url character varying(500) DEFAULT 'https://api.deepseek.com'::character varying,
    failover_priority integer DEFAULT 100,
    daily_limit integer DEFAULT 0,
    used_today integer DEFAULT 0,
    total_used integer DEFAULT 0,
    status character varying(20) DEFAULT 'active'::character varying,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.api_idempotency_keys (
    id bigint NOT NULL,
    idempotency_key character varying(120) NOT NULL,
    route_key character varying(120) NOT NULL,
    request_hash character varying(64) NOT NULL,
    response_body text NOT NULL,
    response_status integer NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.article_distributions (
    id bigint NOT NULL,
    article_id bigint NOT NULL,
    distribution_channel_id bigint NOT NULL,
    action character varying(30) DEFAULT 'publish'::character varying NOT NULL,
    status character varying(30) DEFAULT 'queued'::character varying NOT NULL,
    remote_id character varying(120),
    remote_url character varying(500),
    remote_meta json,
    idempotency_key character varying(120) NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_retry_at timestamp(0) without time zone,
    last_attempt_at timestamp(0) without time zone,
    last_error_message text,
    payload_hash character varying(64),
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.article_images (
    id bigint NOT NULL,
    article_id bigint NOT NULL,
    image_id bigint NOT NULL,
    "position" integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.article_reviews (
    id bigint NOT NULL,
    article_id bigint NOT NULL,
    admin_id bigint NOT NULL,
    review_status character varying(20) NOT NULL,
    review_note text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.articles (
    id bigint NOT NULL,
    title character varying(500) NOT NULL,
    slug character varying(500) NOT NULL,
    excerpt text DEFAULT ''::text,
    content text NOT NULL,
    category_id bigint NOT NULL,
    author_id bigint NOT NULL,
    task_id bigint,
    original_keyword character varying(200) DEFAULT ''::character varying,
    keywords text DEFAULT ''::text,
    meta_description text DEFAULT ''::text,
    status character varying(20) DEFAULT 'draft'::character varying,
    review_status character varying(20) DEFAULT 'pending'::character varying,
    view_count integer DEFAULT 0,
    is_ai_generated integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    published_at timestamp without time zone,
    deleted_at timestamp without time zone,
    is_hot boolean DEFAULT false NOT NULL,
    is_featured boolean DEFAULT false NOT NULL
);

CREATE TABLE public.authors (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    bio text DEFAULT ''::text,
    email character varying(100) DEFAULT ''::character varying,
    avatar character varying(200) DEFAULT ''::character varying,
    website character varying(200) DEFAULT ''::character varying,
    social_links text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.cache (
    key character varying(255) NOT NULL,
    value text NOT NULL,
    expiration integer NOT NULL
);

CREATE TABLE public.cache_locks (
    key character varying(255) NOT NULL,
    owner character varying(255) NOT NULL,
    expiration integer NOT NULL
);

CREATE TABLE public.categories (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    slug character varying(100) NOT NULL,
    description text DEFAULT ''::text,
    sort_order integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.distribution_channel_secrets (
    id bigint NOT NULL,
    distribution_channel_id bigint NOT NULL,
    key_id character varying(80) NOT NULL,
    secret_ciphertext text NOT NULL,
    status character varying(30) DEFAULT 'active'::character varying NOT NULL,
    scopes json,
    last_used_at timestamp(0) without time zone,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.distribution_channels (
    id bigint NOT NULL,
    name character varying(120) NOT NULL,
    domain character varying(255) NOT NULL,
    endpoint_url character varying(500) NOT NULL,
    channel_type character varying(60) DEFAULT 'geoflow_agent'::character varying NOT NULL,
    template_key character varying(120),
    site_settings json,
    channel_config json,
    status character varying(30) DEFAULT 'active'::character varying NOT NULL,
    description text,
    last_health_status character varying(30),
    last_health_checked_at timestamp(0) without time zone,
    last_error_message text,
    created_by_admin_id bigint,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    front_mode character varying(30) DEFAULT 'static'::character varying NOT NULL
);

CREATE TABLE public.distribution_logs (
    id bigint NOT NULL,
    distribution_channel_id bigint,
    article_distribution_id bigint,
    article_id bigint,
    level character varying(20) DEFAULT 'info'::character varying NOT NULL,
    event character varying(120),
    message text NOT NULL,
    context json,
    created_at timestamp(0) without time zone
);

CREATE TABLE public.failed_jobs (
    id bigint NOT NULL,
    uuid character varying(255) NOT NULL,
    connection text NOT NULL,
    queue text NOT NULL,
    payload text NOT NULL,
    exception text NOT NULL,
    failed_at timestamp(0) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.image_libraries (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text,
    image_count integer DEFAULT 0,
    used_task_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.images (
    id bigint NOT NULL,
    library_id bigint NOT NULL,
    filename character varying(255) NOT NULL,
    original_name character varying(255) NOT NULL,
    file_name character varying(255) DEFAULT ''::character varying NOT NULL,
    file_path character varying(500) NOT NULL,
    file_size integer DEFAULT 0,
    mime_type character varying(100) DEFAULT ''::character varying,
    width integer DEFAULT 0,
    height integer DEFAULT 0,
    tags text DEFAULT ''::text,
    used_count integer DEFAULT 0,
    usage_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.job_batches (
    id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    total_jobs integer NOT NULL,
    pending_jobs integer NOT NULL,
    failed_jobs integer NOT NULL,
    failed_job_ids text NOT NULL,
    options text,
    cancelled_at integer,
    created_at integer NOT NULL,
    finished_at integer
);

CREATE TABLE public.jobs (
    id bigint NOT NULL,
    queue character varying(255) NOT NULL,
    payload text NOT NULL,
    attempts smallint NOT NULL,
    reserved_at integer,
    available_at integer NOT NULL,
    created_at integer NOT NULL
);

CREATE TABLE public.keyword_libraries (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text,
    keyword_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.keywords (
    id bigint NOT NULL,
    library_id bigint NOT NULL,
    keyword character varying(200) NOT NULL,
    used_count integer DEFAULT 0,
    usage_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.knowledge_bases (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text,
    content text NOT NULL,
    character_count integer DEFAULT 0,
    used_task_count integer DEFAULT 0,
    file_type character varying(20) DEFAULT 'markdown'::character varying,
    file_path character varying(500) DEFAULT ''::character varying,
    word_count integer DEFAULT 0,
    usage_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    source_name character varying(150),
    source_url character varying(500),
    source_type character varying(50) DEFAULT 'document'::character varying NOT NULL,
    business_line character varying(100),
    effective_date date,
    risk_level character varying(20) DEFAULT 'medium'::character varying NOT NULL,
    review_status character varying(20) DEFAULT 'unreviewed'::character varying NOT NULL
);

CREATE TABLE public.migrations (
    id integer NOT NULL,
    migration character varying(255) NOT NULL,
    batch integer NOT NULL
);

CREATE TABLE public.password_reset_tokens (
    email character varying(255) NOT NULL,
    token character varying(255) NOT NULL,
    created_at timestamp(0) without time zone
);

CREATE TABLE public.personal_access_tokens (
    id bigint NOT NULL,
    tokenable_type character varying(255) NOT NULL,
    tokenable_id bigint NOT NULL,
    name text NOT NULL,
    token character varying(64) NOT NULL,
    abilities text,
    last_used_at timestamp(0) without time zone,
    expires_at timestamp(0) without time zone,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.prompts (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    type character varying(50) NOT NULL,
    content text NOT NULL,
    variables text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.sensitive_words (
    id bigint NOT NULL,
    word character varying(100) NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.sessions (
    id character varying(255) NOT NULL,
    user_id bigint,
    ip_address character varying(45),
    user_agent text,
    payload text NOT NULL,
    last_activity integer NOT NULL
);

CREATE TABLE public.site_settings (
    id bigint NOT NULL,
    setting_key character varying(100) NOT NULL,
    setting_value text,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

COMMENT ON COLUMN public.site_settings.id IS '主键';

COMMENT ON COLUMN public.site_settings.setting_key IS '配置键，唯一';

COMMENT ON COLUMN public.site_settings.setting_value IS '配置值（文本/JSON 字符串）';

CREATE TABLE public.system_logs (
    id bigint NOT NULL,
    type character varying(50) NOT NULL,
    message text NOT NULL,
    data text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.system_update_backups (
    id bigint NOT NULL,
    backup_uuid character varying(64) NOT NULL,
    run_id bigint,
    from_version character varying(50),
    to_version character varying(50),
    from_commit character varying(80),
    to_commit character varying(80),
    backup_path character varying(500) NOT NULL,
    manifest_path character varying(500) NOT NULL,
    files_archive_path character varying(500),
    database_dump_path character varying(500),
    file_count integer DEFAULT 0 NOT NULL,
    total_bytes bigint DEFAULT '0'::bigint NOT NULL,
    status character varying(30) DEFAULT 'available'::character varying NOT NULL,
    created_by_admin_id bigint,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.system_update_runs (
    id bigint NOT NULL,
    run_uuid character varying(64) NOT NULL,
    action character varying(30) NOT NULL,
    status character varying(30) NOT NULL,
    current_version character varying(50),
    target_version character varying(50),
    current_commit character varying(80),
    target_commit character varying(80),
    deployment_mode character varying(60),
    risk_level character varying(20),
    plan_json json,
    plan_path character varying(500),
    backup_path character varying(500),
    log_path character varying(500),
    error_message text,
    started_by_admin_id bigint,
    started_at timestamp(0) without time zone,
    finished_at timestamp(0) without time zone,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.task_distribution_channels (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    distribution_channel_id bigint NOT NULL,
    trigger character varying(60) DEFAULT 'after_local_publish'::character varying NOT NULL,
    remote_status character varying(40) DEFAULT 'follow_local'::character varying NOT NULL,
    failure_policy character varying(60) DEFAULT 'ignore_distribution_failure'::character varying NOT NULL,
    max_attempts smallint DEFAULT '3'::smallint NOT NULL,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.task_runs (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    status character varying(20) NOT NULL,
    article_id bigint,
    error_message text DEFAULT ''::text,
    duration_ms integer DEFAULT 0,
    meta text DEFAULT ''::text,
    started_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    finished_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.task_schedules (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    next_run_time timestamp without time zone NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying,
    error_message text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.tasks (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    title_library_id bigint NOT NULL,
    image_library_id bigint,
    image_count integer DEFAULT 1,
    prompt_id bigint NOT NULL,
    ai_model_id bigint NOT NULL,
    author_id bigint,
    need_review integer DEFAULT 1,
    publish_interval integer DEFAULT 3600,
    author_type character varying(20) DEFAULT 'random'::character varying,
    custom_author_id bigint,
    auto_keywords integer DEFAULT 1,
    auto_description integer DEFAULT 1,
    draft_limit integer DEFAULT 10,
    article_limit integer DEFAULT 10,
    is_loop integer DEFAULT 0,
    model_selection_mode character varying(20) DEFAULT 'fixed'::character varying,
    status character varying(20) DEFAULT 'active'::character varying,
    created_count integer DEFAULT 0,
    published_count integer DEFAULT 0,
    loop_count integer DEFAULT 0,
    knowledge_base_id bigint,
    category_mode character varying(20) DEFAULT 'smart'::character varying,
    fixed_category_id bigint,
    last_run_at timestamp without time zone,
    next_run_at timestamp without time zone,
    next_publish_at timestamp without time zone,
    last_success_at timestamp without time zone,
    last_error_at timestamp without time zone,
    last_error_message text DEFAULT ''::text,
    schedule_enabled integer DEFAULT 1,
    max_retry_count integer DEFAULT 3,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    publish_scope character varying(40) DEFAULT 'local_and_distribution'::character varying NOT NULL
);

CREATE TABLE public.title_libraries (
    id bigint NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text,
    title_count integer DEFAULT 0,
    generation_type character varying(20) DEFAULT 'manual'::character varying,
    keyword_library_id bigint,
    ai_model_id bigint,
    prompt_id bigint,
    generation_rounds integer DEFAULT 1,
    is_ai_generated integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.titles (
    id bigint NOT NULL,
    library_id bigint NOT NULL,
    title character varying(500) NOT NULL,
    keyword character varying(200) DEFAULT ''::character varying,
    is_ai_generated boolean DEFAULT false,
    used_count integer DEFAULT 0,
    usage_count integer DEFAULT 0,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.url_import_job_logs (
    id bigint NOT NULL,
    job_id bigint NOT NULL,
    step character varying(50) DEFAULT 'queued'::character varying,
    level character varying(20) DEFAULT 'info'::character varying,
    message text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.url_import_jobs (
    id bigint NOT NULL,
    url text NOT NULL,
    normalized_url text NOT NULL,
    source_domain character varying(255) DEFAULT ''::character varying,
    page_title character varying(255) DEFAULT ''::character varying,
    status character varying(20) DEFAULT 'queued'::character varying,
    current_step character varying(50) DEFAULT 'queued'::character varying,
    progress_percent integer DEFAULT 0,
    options_json text DEFAULT ''::text,
    result_json text DEFAULT ''::text,
    error_message text DEFAULT ''::text,
    created_by character varying(100) DEFAULT ''::character varying,
    started_at timestamp without time zone,
    finished_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.users (
    id bigint NOT NULL,
    name character varying(255) NOT NULL,
    email character varying(255) NOT NULL,
    email_verified_at timestamp(0) without time zone,
    password character varying(255) NOT NULL,
    remember_token character varying(100),
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

CREATE TABLE public.view_logs (
    id bigint NOT NULL,
    article_id bigint,
    source character varying(32) DEFAULT 'local'::character varying NOT NULL,
    method character varying(16) DEFAULT 'GET'::character varying NOT NULL,
    path character varying(2048) DEFAULT ''::character varying NOT NULL,
    route_name character varying(128),
    status_code smallint DEFAULT '200'::smallint NOT NULL,
    ip_address character varying(64) DEFAULT ''::character varying NOT NULL,
    user_agent text,
    referer character varying(2048),
    created_at timestamp(0) without time zone
);

CREATE TABLE public.worker_heartbeats (
    worker_id character varying(100) NOT NULL,
    status character varying(20) DEFAULT 'idle'::character varying NOT NULL,
    last_seen_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    meta text DEFAULT ''::text,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
);
