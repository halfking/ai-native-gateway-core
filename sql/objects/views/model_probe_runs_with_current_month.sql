--
-- Name: model_probe_runs_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_probe_runs_with_current_month AS
 SELECT model_probe_runs_hot.id,
    model_probe_runs_hot.tenant_id,
    model_probe_runs_hot.credential_id,
    model_probe_runs_hot.raw_model_name,
    model_probe_runs_hot.status,
    model_probe_runs_hot.http_status,
    model_probe_runs_hot.error_code,
    model_probe_runs_hot.error_message,
    model_probe_runs_hot.latency_ms,
    model_probe_runs_hot.state_change,
    model_probe_runs_hot.state_applied,
    model_probe_runs_hot.triggered_by,
    model_probe_runs_hot.created_at
   FROM public.model_probe_runs_hot
UNION ALL
 SELECT model_probe_runs.id,
    model_probe_runs.tenant_id,
    model_probe_runs.credential_id,
    model_probe_runs.raw_model_name,
    model_probe_runs.status,
    model_probe_runs.http_status,
    model_probe_runs.error_code,
    model_probe_runs.error_message,
    model_probe_runs.latency_ms,
    model_probe_runs.state_change,
    model_probe_runs.state_applied,
    model_probe_runs.triggered_by,
    model_probe_runs.created_at
   FROM public.model_probe_runs;


--
-- Name: VIEW model_probe_runs_with_current_month; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_probe_runs_with_current_month IS 'Optimized query VIEW using hot table architecture.
- model_probe_runs_hot: independent hot table (default 24h retention, configurable)
- model_probe_runs: parent table (auto-aggregates all ATTACHED monthly partitions, columnar storage)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 386 (2026-07-11).';

