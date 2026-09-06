-- 677_session_summaries_canonical_bootstrap.sql
-- 2026-09-07: 在没有 public.session_summaries 的共享库里 bootstrap canonical 表
--
-- 事故背景（2026-09-07 本地部署报 "database revision sequence failed:
-- public.session_summaries is missing"）：
--   共享 PG 实例 llm-gateway-pg 同时承载 llm-gateway-go 与 memora/kxmemory
--   等其他产品。apply_schema_if_empty() 因为数据库里 700+ 张 memora 表已存
--   在而跳过整份 sql/schema/01-schema.sql 快照（条件：count(*) == 0）。
--   后续 apply-db-revision-sequence.sh 又以 "base relation missing" 拒绝运
--   行 — 共享实例同库是设计常态（不要清库），但 partial schema 通道从来没
--   人补过：memora 是用自身最小结构覆盖 session_summaries，gateway 这台
--   的 session_summaries 则完全没有 memora 写过、又被 01-schema.sql 跳过，
--   所以表从一开始就不存在。
--
-- 修复策略（最小侵入，仅 01-schema.sql:15000 起那段 canonical DDL 的精简版）：
--   - 仅在 to_regclass('public.session_summaries') IS NULL 时创建。
--     用 IF NOT EXISTS 防止后续被另一产品（memora 临时表等）覆盖时误删。
--   - 列序与 01-schema.sql:15000-15051 完全一致；DEFAULT/NULL/GENERATED 语义
--     不动；不引入 655+ 才能加的扩展列（gw_project_id/search_vector 等留给
--     655 / 606 / 483 后续补齐）。
--   - 约束只保留 01-schema.sql 自带的 quality_score CHECK；其它 655 段落的
--     UNIQUE 索引 / 跨租户约束 / gin 索引留给 655 顺序完成（先创建表，再
--     走 reconcile 链路）。
--   - 如果表已存在（memora 最小结构、shared schema 等）则整个文件 no-op，
--     不破坏任何已有列。
--
-- 幂等：是（CREATE TABLE IF NOT EXISTS + 包裹 DO $$ 守卫）。

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.session_summaries') IS NULL THEN
        CREATE TABLE public.session_summaries (
            session_key character varying(255) NOT NULL,
            tenant_id character varying(255) NOT NULL,
            first_request_at timestamp with time zone NOT NULL,
            last_request_at timestamp with time zone NOT NULL,
            duration_seconds integer GENERATED ALWAYS AS ((EXTRACT(epoch FROM (last_request_at - first_request_at)))::integer) STORED,
            request_count integer DEFAULT 0 NOT NULL,
            success_count integer DEFAULT 0 NOT NULL,
            error_count integer DEFAULT 0 NOT NULL,
            total_cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
            input_cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
            output_cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
            total_prompt_tokens bigint DEFAULT 0 NOT NULL,
            total_completion_tokens bigint DEFAULT 0 NOT NULL,
            total_tokens bigint GENERATED ALWAYS AS (total_prompt_tokens + total_completion_tokens) STORED,
            avg_latency_ms integer DEFAULT 0 NOT NULL,
            min_latency_ms integer,
            max_latency_ms integer,
            models_used text[] DEFAULT '{}'::text[] NOT NULL,
            primary_model character varying(100),
            model_switch_count integer DEFAULT 0 NOT NULL,
            title character varying(200),
            summary text,
            key_topics text[],
            user_intent character varying(50),
            quality_score integer,
            compliance_status character varying(20) DEFAULT 'compliant'::character varying,
            compliance_issues_count integer DEFAULT 0 NOT NULL,
            prompt_injection_detected boolean DEFAULT false,
            pii_detected boolean DEFAULT false,
            toxic_output_detected boolean DEFAULT false,
            work_types text[],
            providers text[],
            client_models text[],
            last_summarized_at timestamp with time zone,
            summary_version integer DEFAULT 1,
            created_at timestamp with time zone DEFAULT now(),
            updated_at timestamp with time zone DEFAULT now(),
            handoff_count integer,
            last_handoff_at timestamp without time zone,
            health_score integer,
            health_grade character varying(1),
            range character varying(20),
            last_health_at timestamp without time zone,
            tokens_at_trigger bigint DEFAULT 0 NOT NULL,
            messages_at_trigger integer DEFAULT 0 NOT NULL,
            last_trigger_reason character varying(64),
            last_trigger_at timestamp with time zone,
            parent_session_key character varying(255) DEFAULT ''::character varying,
            handoff_reason character varying(64) DEFAULT ''::character varying,
            CONSTRAINT session_summaries_quality_score_check CHECK (((quality_score >= 0) AND (quality_score <= 10)))
        );
        RAISE NOTICE 'session_summaries bootstrap: created canonical table';
    ELSE
        RAISE NOTICE 'session_summaries bootstrap: table already exists, no-op';
    END IF;
END $$;

COMMIT;