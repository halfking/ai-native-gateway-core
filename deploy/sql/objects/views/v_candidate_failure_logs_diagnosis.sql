--
-- Name: v_candidate_failure_logs_diagnosis; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_candidate_failure_logs_diagnosis AS
 SELECT id,
    ts,
    tenant_id,
    credential_id,
    provider_id,
    raw_model_name,
    attempt_index,
    error_kind AS legacy_kind,
    COALESCE(upstream_status_code,
        CASE
            WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
            ELSE NULL::integer
        END) AS extracted_upstream_status_code,
    public.diagnose_failure_kind(COALESCE(upstream_status_code,
        CASE
            WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
            ELSE NULL::integer
        END), COALESCE(NULLIF(upstream_response_body, ''::text), error_message, ''::text)) AS diagnosed_error_kind,
    upstream_status_code AS live_upstream_status_code,
    latency_ms,
    per_attempt_latency_ms,
    retryable,
    error_message,
    (error_kind IS DISTINCT FROM public.diagnose_failure_kind(COALESCE(upstream_status_code,
        CASE
            WHEN (error_message ~ 'upstream [0-9]+:'::text) THEN ("substring"(error_message, 'upstream ([0-9]+):'::text))::integer
            ELSE NULL::integer
        END), COALESCE(NULLIF(upstream_response_body, ''::text), error_message, ''::text))) AS classification_disagrees
   FROM public.candidate_failure_logs cfl;


--
-- Name: VIEW v_candidate_failure_logs_diagnosis; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_candidate_failure_logs_diagnosis IS '2026-06-30 (migration 057). Computes the post-P2 classifier
     output (diagnosed_error_kind) for every candidate_failure_logs
     row, recovering the upstream HTTP status code from
     error_message via the "upstream NNN:" regex. Side-by-side
     legacy_kind vs diagnosed_error_kind for incident review.
     Companion to v_request_failures_diagnosis (migration 056).';

