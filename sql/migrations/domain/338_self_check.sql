-- Migration 338: 系统自检（Self-Check）模块
-- Date: 2026-07-11
-- Purpose: 定期对关键模型跑 ping + 3 轮工具调用会话测试，验证 gateway 可用性；
--          失败时用 provider 原始凭据直连上游做故障隔离。
--
-- 背景：当前只能被动感知故障（用户报错）；缺少对 Top 模型 + 工具调用的主动自检；
--       故障定位困难（不知道是 gateway 还是上游问题）。
-- 本模块：自动注册一个 system api_key（通过 is_system=true），worker 用此 key
--        调用本地 gateway 进行自检。
-- 设计文档：docs/自检功能/01-design.md

BEGIN;

-- 1. 自检运行主表
CREATE TABLE IF NOT EXISTS self_check_runs (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    model_name          text NOT NULL,
    started_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    duration_ms         integer NOT NULL DEFAULT 0,
    status              text NOT NULL DEFAULT 'running',        -- running/success/partial/failed
    rounds_total        integer NOT NULL DEFAULT 3,
    rounds_success      integer NOT NULL DEFAULT 0,
    had_tool_call       boolean NOT NULL DEFAULT false,
    total_tokens        integer NOT NULL DEFAULT 0,
    avg_latency_ms      integer NOT NULL DEFAULT 0,
    error_type          text,                                    -- http_000/http_502/http_503/timeout/upstream_fail/none
    error_detail        text,
    upstream_tested     boolean NOT NULL DEFAULT false,           -- 失败时是否测了上游
    upstream_result     text,                                    -- success/failed/timeout/no_credential
    upstream_latency_ms integer,
    upstream_error      text,
    tenant_id           text NOT NULL DEFAULT 'default',
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT self_check_runs_status_check CHECK (status IN ('running','success','partial','failed')),
    CONSTRAINT self_check_runs_error_type_check CHECK (error_type IS NULL OR error_type IN ('http_000','http_502','http_503','http_504','timeout','upstream_fail','none'))
);

CREATE INDEX IF NOT EXISTS idx_self_check_runs_model ON self_check_runs(model_name);
CREATE INDEX IF NOT EXISTS idx_self_check_runs_started ON self_check_runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_self_check_runs_status ON self_check_runs(status);
CREATE INDEX IF NOT EXISTS idx_self_check_runs_model_started ON self_check_runs(model_name, started_at DESC);

COMMENT ON TABLE self_check_runs IS '系统自检每次完整运行的汇总记录';
COMMENT ON COLUMN self_check_runs.status IS 'running=执行中 / success=3轮全成功 / partial=部分成功 / failed=3轮全失败';
COMMENT ON COLUMN self_check_runs.upstream_result IS '故障隔离结果：success/failed/timeout/no_credential';


-- 2. 每轮详细结果
CREATE TABLE IF NOT EXISTS self_check_round_results (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id              bigint NOT NULL REFERENCES self_check_runs(id) ON DELETE CASCADE,
    round_index         integer NOT NULL,                         -- 0=ping, 1/2/3=对话
    is_ping             boolean NOT NULL DEFAULT false,
    is_tool_call        boolean NOT NULL DEFAULT false,
    latency_ms          integer NOT NULL DEFAULT 0,
    prompt_tokens       integer NOT NULL DEFAULT 0,
    completion_tokens   integer NOT NULL DEFAULT 0,
    total_tokens        integer NOT NULL DEFAULT 0,
    success             boolean NOT NULL DEFAULT false,
    http_code           integer,
    error_message       text,
    request_body        text,                                     -- 截断到 4K
    response_preview    text,                                     -- 前 500 字符
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT self_check_round_results_round_check CHECK (round_index >= 0 AND round_index <= 10)
);

CREATE INDEX IF NOT EXISTS idx_self_check_rounds_run ON self_check_round_results(run_id);

COMMENT ON TABLE self_check_round_results IS '自检每轮（ping 或对话）的详细结果';
COMMENT ON COLUMN self_check_round_results.is_ping IS '是否 ping 轮（round_index=0）';
COMMENT ON COLUMN self_check_round_results.is_tool_call IS '是否包含工具调用';


-- 3. 自检配置（单行）
CREATE TABLE IF NOT EXISTS self_check_settings (
    id                          integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled                     boolean NOT NULL DEFAULT true,
    normal_interval_seconds     integer NOT NULL DEFAULT 60,
    fault_interval_seconds      integer NOT NULL DEFAULT 30,
    model_source                text NOT NULL DEFAULT 'both',    -- top10/featured/both
    max_models                  integer NOT NULL DEFAULT 10,
    max_tokens_per_run          integer NOT NULL DEFAULT 100000,
    featured_model_ids          jsonb NOT NULL DEFAULT '[]'::jsonb,
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    updated_by                  text,

    CONSTRAINT self_check_settings_model_source_check CHECK (model_source IN ('top10','featured','both')),
    CONSTRAINT self_check_settings_interval_check CHECK (normal_interval_seconds >= 10 AND fault_interval_seconds >= 5)
);

-- 默认开启 + 注入默认特色模型
INSERT INTO self_check_settings (id, featured_model_ids)
VALUES (1, '["minimax-m2.7","glm-5.2","mimo-v2.5","claude-sonnet-5","gpt-5.4","gpt-5.6-luna","deepseek-v4-pro"]'::jsonb)
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE self_check_settings IS '系统自检模块配置（单行）';
COMMENT ON COLUMN self_check_settings.featured_model_ids IS '特色模型 canonical_name 列表，worker 会与 Top10 去重合并';


-- 4. 数据保留策略：30 天前的记录自动清理（通过 bg worker）
--    （不创建 cron job，由独立的 cleanup worker 每天扫一次）

COMMIT;