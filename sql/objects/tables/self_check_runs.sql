--
-- Name: self_check_runs; Type: TABLE; Schema: public; Owner: -
--

-- ★ 2026-11-02（12h 审计轮 P1）订正：error_type 的 CHECK 此前记的是**窄集 7 值**
--   （http_000/502/503/504 + timeout/upstream_fail/none），而迁移 644 起的
--   **宽集**才是线上实际生效的形态 ⇒ 两份文档互相矛盾 ⇒ 后人照本文件推演
--   会得出「error_type 只允许 7 种取值」的错误结论。
--   ★ 本文件现与 `sql/migrations/startup/930_self_check_error_type_kind_closure_model_unavailable.sql`
--   对齐：errorsx.ErrorKind 全闭集（**33 kind**）+ worker 专用字面量（5 个）
--     + 历史 http_* 诊断值（用 LIKE 'http_%' 通配，不再逐个枚举）。
--   ★ 2026-11-03 同步轮订正：930 又补了 `model_unavailable`
--     （errorsx 加 `KindModelUnavailable` 的那一笔 `4fe043dc3` 比 929 的落地
--     晚 5 小时 ⇒ 闭集少 1 项）⇒ 本文件同步从 37 项长到 38 项。
--   ★ 2026-11-11 闭集门第三次复发订正：957 又补了 `rate_limit_observed`
--     （审计 F50 轮加的 `errorsx.KindRateLimitObserved` —— 「上游用请求侧 4xx
--     回了限流形态正文」⇒ 归因到「**不惩罚节点**」的独立 kind，
--     与 `KindRateLimit` 的 `PenaltyBinding` 明确不同）⇒ 本文件同步从 38 项
--     长到 **39 项**（34 kind + 5 worker 专用字面量）。
--   ★ 这是同一个「镜像面没跟上」家族的**第三次**复发：929 补 6 个 ⇒
--     930 补 `model_unavailable` ⇒ 957 补 `rate_limit_observed`。
--   ★ 本文件现与 `sql/migrations/startup/957_self_check_error_type_kind_closure_rate_limit_observed.sql`
--     对齐（930 那份仍是安装顺序里最后一个**已含** `model_unavailable` 的版本；
--     957 是最新的重建者）。
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
    CONSTRAINT self_check_runs_error_type_check CHECK (((error_type IS NULL) OR (error_type ~~ 'http_%'::text) OR (error_type = ANY (ARRAY['account_restricted'::text, 'auth'::text, 'auth_revoked'::text, 'canceled'::text, 'circuit_open'::text, 'client_bug'::text, 'concurrent'::text, 'content_filter'::text, 'context_length_exceeded'::text, 'conversion_error'::text, 'empty_response'::text, 'first_byte_timeout'::text, 'fp_slot_saturated'::text, 'internal'::text, 'model_deprecated'::text, 'model_not_found'::text, 'model_unavailable'::text, 'network'::text, 'no_available_channel'::text, 'none'::text, 'parse_error'::text, 'quota'::text, 'quota_balance'::text, 'quota_periodic'::text, 'quota_permanent'::text, 'rate_limit'::text, 'rate_limit_observed'::text, 'reply_loop'::text, 'request_rejected'::text, 'stream_timeout'::text, 'timeout'::text, 'tool_call_id_mismatch'::text, 'transient'::text, 'unattributed'::text, 'upstream_context_loss'::text, 'upstream_down'::text, 'upstream_fail'::text, 'upstream_overloaded'::text, 'unsupported_feature'::text])))),
    CONSTRAINT self_check_runs_selection_strategy_check CHECK (((selection_strategy IS NULL) OR (selection_strategy ~~ 'most_used'::text) OR (selection_strategy ~~ 'fallback_%'::text) OR (selection_strategy = 'random'::text))),
    CONSTRAINT self_check_runs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'success'::text, 'partial'::text, 'failed'::text, 'retrying'::text])))
);

