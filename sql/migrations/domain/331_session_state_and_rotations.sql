-- Migration 331: session_state_snapshots & session_credential_rotations
-- 2026-07-06: 会话状态快照与凭据轮换审计表
-- Ref: docs/llm-gateway-go/session-state-management-plan.md

-- ──── session_state_snapshots: 会话状态快照（停止时写入） ────
CREATE TABLE IF NOT EXISTS public.session_state_snapshots (
    id                      bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id              text         NOT NULL UNIQUE,
    tenant_id               text         NOT NULL,
    api_key_id              bigint       NOT NULL,
    task_id                 text,
    client_ip               text,
    client_fp               text,
    client_profile          text,
    status                  text         NOT NULL DEFAULT 'active',
    created_at              timestamptz  NOT NULL,
    first_request_at        timestamptz,
    last_request_at         timestamptz,
    stopped_at              timestamptz,
    stop_reason             text,
    recovered_at            timestamptz,
    final_credential_id     bigint,
    final_model             text,
    final_provider          text,
    total_turns             integer      NOT NULL DEFAULT 0,
    total_duration_sec      integer      NOT NULL DEFAULT 0,
    total_prompt_tokens     bigint       NOT NULL DEFAULT 0,
    total_completion_tokens bigint       NOT NULL DEFAULT 0,
    total_cost_usd          numeric(14,8) NOT NULL DEFAULT 0,
    title                   text,
    summary                 text,
    annotation              text,
    tags                    text[]       NOT NULL DEFAULT '{}',
    fp_slot_index           integer,
    raw_snapshot            jsonb,
    created_at_db           timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sss_tenant ON public.session_state_snapshots (tenant_id, stopped_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_sss_api_key ON public.session_state_snapshots (api_key_id, stopped_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_sss_status ON public.session_state_snapshots (status) WHERE status IN ('active', 'stopped');
CREATE INDEX IF NOT EXISTS idx_sss_credential ON public.session_state_snapshots (final_credential_id, stopped_at DESC);

COMMENT ON TABLE public.session_state_snapshots IS '会话状态快照，会话停止或结束时写入，用于审计与历史查询';

-- ──── session_credential_rotations: 凭据轮换审计 ────
CREATE TABLE IF NOT EXISTS public.session_credential_rotations (
    id                  bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id          text         NOT NULL,
    tenant_id           text         NOT NULL,
    seq                 integer      NOT NULL,
    credential_id       bigint       NOT NULL,
    credential_label    text,
    model               text,
    provider            text,
    started_at          timestamptz  NOT NULL,
    ended_at            timestamptz,
    turns               integer      NOT NULL DEFAULT 0,
    duration_sec        integer      NOT NULL DEFAULT 0,
    prompt_tokens       bigint       NOT NULL DEFAULT 0,
    completion_tokens   bigint       NOT NULL DEFAULT 0,
    cost_usd            numeric(14,8) NOT NULL DEFAULT 0,
    switch_reason       text         NOT NULL DEFAULT 'initial',
    fp_slot_index       integer,
    created_at          timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT session_credential_rotations_session_seq UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_scr_session ON public.session_credential_rotations (session_id, seq);
CREATE INDEX IF NOT EXISTS idx_scr_credential ON public.session_credential_rotations (credential_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_scr_tenant ON public.session_credential_rotations (tenant_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_scr_model ON public.session_credential_rotations (model, started_at DESC);

COMMENT ON TABLE public.session_credential_rotations IS '凭据轮换审计，记录会话内每次凭据切换的持续时间、轮次、token、费用';
