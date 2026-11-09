--
-- Name: session_turns_probe_hot; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/928_probe_traffic_physical_lane.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-11-02 补录（探测物理隔离车道）：迁移 928 创建的探测流水热表此前在正典
--   目录里没有任何条目 ⇒ 「照正典文件读会话族」的人读出一张不存在的表。
--
-- ★ 本表与业务热表 session_turns_hot 是**两条互不相通的写路径**：
--   物理隔离车道开启（LLM_GATEWAY_PROBE_LANE_PHYSICAL=1）时，探测流量写本表、
--   **不写** session_turns / session_turns_hot / usage_ledger_hot；
--   开关关闭（默认）时本表恒为空，探测流量仍走业务表。
--   ⇒ 读本表前必须确认开关状态，否则会把「空表」误读成「探测没发生」。
--
-- ★ TTL：bg.PartitionManager.cleanupOldProbeLaneTables（默认 14 天，
--   lifecycle.session_turns_probe_hot_ttl_days 热加载）。无月分区、以热表
--   + 短 TTL 为终态（方案 §0）。

CREATE TABLE public.session_turns_probe_hot (
    request_id         text PRIMARY KEY,
    ts                 timestamptz NOT NULL DEFAULT now(),
    tenant_id          text NOT NULL DEFAULT 'default',
    credential_id      text,
    raw_model_name     text,
    client_model       text,
    outbound_model     text,
    success            boolean,
    status_code        integer,
    error_kind         text,
    latency_ms         integer,
    prompt_tokens      integer,
    completion_tokens  integer,
    total_tokens       integer,
    origin_stage       text,
    origin_actor       text,
    task_type          text,
    api_key_id         text,
    provider_id        bigint,
    client_ip          text,
    request_preview    text,
    gw_session_id      text,
    agent_name         text,
    quality_flags      text[],
    created_at         timestamptz NOT NULL DEFAULT now(),
    trace_events       jsonb
);


--
-- Name: INDEX idx_session_turns_probe_hot_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_probe_hot_ts
    ON public.session_turns_probe_hot (ts DESC);

--
-- Name: INDEX idx_session_turns_probe_hot_stage_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_probe_hot_stage_ts
    ON public.session_turns_probe_hot (origin_stage, ts DESC);

--
-- Name: INDEX idx_session_turns_probe_hot_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_turns_probe_hot_tenant_ts
    ON public.session_turns_probe_hot (tenant_id, ts DESC);


--
-- Name: TABLE session_turns_probe_hot; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_turns_probe_hot IS 'Probe-lane request ledger (physical isolation from session_turns_hot). Writers gated by LLM_GATEWAY_PROBE_LANE_PHYSICAL.';
