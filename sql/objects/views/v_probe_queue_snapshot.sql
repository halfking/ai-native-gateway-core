--
-- Name: v_probe_queue_snapshot; Type: VIEW; Schema: public; Owner: -
--
-- 2026-09-17 数据源统一:改读 v_node_probe_state_compat(node_probe_state 投影)。
-- 列顺序与旧契约完全一致。SSOT: db.ensureProbeHealthDashboardViews(db/db.go)。
--

CREATE VIEW public.v_probe_queue_snapshot AS
 SELECT probe_priority,
    state,
    count(*) AS queue_size,
    count(*) FILTER (WHERE (next_retry_at <= now())) AS ready_now,
    count(*) FILTER (WHERE (next_retry_at <= (now() + '00:01:00'::interval))) AS ready_1min,
    count(*) FILTER (WHERE (next_retry_at <= (now() + '00:05:00'::interval))) AS ready_5min,
    min(next_retry_at) AS earliest_retry_at,
    max(next_retry_at) AS latest_retry_at,
    avg(EXTRACT(epoch FROM (now() - last_attempt_at))) AS avg_wait_seconds,
    max(EXTRACT(epoch FROM (now() - last_attempt_at))) AS max_wait_seconds
   FROM ( SELECT
                CASE
                    WHEN (mps.consecutive_failures >= 3) THEN 'urgent'::text
                    WHEN (mps.state = 'suspicious'::text) THEN 'suspicious'::text
                    WHEN (mps.state = 'broken_confirmed'::text) THEN 'failing'::text
                    ELSE NULL::text
                END AS probe_priority,
                mps.state,
                mps.next_retry_at,
                mps.last_attempt_at
           FROM (public.v_node_probe_state_compat mps
             JOIN public.credentials c ON ((c.id = mps.credential_id)))
          WHERE ((mps.state = ANY (ARRAY['suspicious'::text, 'broken_confirmed'::text])) AND (mps.paused = false) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false))
        ) sub
  GROUP BY probe_priority, state
  ORDER BY
        CASE
            WHEN (probe_priority = 'urgent'::text) THEN 1
            WHEN (probe_priority = 'suspicious'::text) THEN 2
            WHEN (probe_priority = 'failing'::text) THEN 3
            WHEN (probe_priority = 'watchdog'::text) THEN 4
            ELSE 5
        END, state;
