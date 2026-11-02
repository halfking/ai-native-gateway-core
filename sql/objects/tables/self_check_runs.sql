--
-- Name: self_check_runs; Type: TABLE; Schema: public; Owner: -
--

-- ★ 2026-11-02（12h 审计轮 P1）订正：error_type 的 CHECK 此前记的是**窄集 7 值**
--   （http_000/502/503/504 + timeout/upstream_fail/none），而迁移 644 起的
--   **宽集**才是线上实际生效的形态 ⇒ 两份文档互相矛盾 ⇒ 后人照本文件推演
--   会得出「error_type 只允许 7 种取值」的错误结论。
--   ★ 本文件现与 `sql/migrations/startup/929_self_check_error_type_kind_closure.sql`
--   对齐：errorsx.ErrorKind 全闭集（32 kind）+ worker 专用字面量（5 个）
--     + 历史 http_* 诊断值（用 LIKE 'http_%' 通配，不再逐个枚举）。
--   ★ 交叉门（改本文件或加新 kind 时会红）：
--     sql/migrations/startup/migration_929_error_type_closure_test.go
--   ★ 写点对照（决定哪些字面量必须在场）：
--     kind 名       → bg/credential_selfcheck.go:844（ClassifyErrorWithBody）
--     'none'        → bg/self_check_worker.go:550 / bg/credential_selfcheck.go:388
--     'internal'    → bg/self_check_worker.go:634 / bg/credential_selfcheck.go:813
--     'parse_error' → bg/self_check_worker.go:690
--     'unattributed'→ bg/credential_selfcheck.go:808
--
CREATE TABLE public.self_check_runs (
    id bigint NOT NULL,
    model_name text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    duration_ms integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    rounds_total integer DEFAULT 3 NOT NULL,
    rounds_success integer DEFAULT 0 NOT NULL,
    had_tool_call boolean DEFAULT false NOT NULL,
    total_tokens integer DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0 NOT NULL,
    error_type text,
    error_detail text,
    upstream_tested boolean DEFAULT false NOT NULL,
    upstream_result text,
    upstream_latency_ms integer,
    upstream_error text,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    selection_strategy text DEFAULT 'most_used'::text,
    attempted_models jsonb DEFAULT '[]'::jsonb,
    CONSTRAINT self_check_runs_error_type_check CHECK (((error_type IS NULL) OR (error_type ~~ 'http_%'::text) OR (error_type = ANY (ARRAY['account_restricted'::text, 'auth'::text, 'auth_revoked'::text, 'canceled'::text, 'circuit_open'::text, 'client_bug'::text, 'concurrent'::text, 'content_filter'::text, 'context_length_exceeded'::text, 'conversion_error'::text, 'empty_response'::text, 'first_byte_timeout'::text, 'fp_slot_saturated'::text, 'internal'::text, 'model_deprecated'::text, 'model_not_found'::text, 'model_unavailable'::text, 'network'::text, 'no_available_channel'::text, 'none'::text, 'parse_error'::text, 'quota'::text, 'quota_balance'::text, 'quota_periodic'::text, 'quota_permanent'::text, 'rate_limit'::text, 'reply_loop'::text, 'request_rejected'::text, 'stream_timeout'::text, 'timeout'::text, 'tool_call_id_mismatch'::text, 'transient'::text, 'unattributed'::text, 'upstream_context_loss'::text, 'upstream_down'::text, 'upstream_fail'::text, 'upstream_overloaded'::text, 'unsupported_feature'::text])))),
    CONSTRAINT self_check_runs_selection_strategy_check CHECK (((selection_strategy IS NULL) OR (selection_strategy ~~ 'most_used'::text) OR (selection_strategy ~~ 'fallback_%'::text) OR (selection_strategy = 'random'::text))),
    CONSTRAINT self_check_runs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'success'::text, 'partial'::text, 'failed'::text, 'retrying'::text])))
);

