--
-- Name: request_wal_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.request_wal_with_current_month AS
 SELECT request_wal_hot.request_id,
    request_wal_hot.tenant_id,
    request_wal_hot.gw_session_id,
    request_wal_hot.status,
    request_wal_hot.stage,
    request_wal_hot.client_model,
    request_wal_hot.upstream_provider_id,
    request_wal_hot.upstream_credential_id,
    request_wal_hot.completion_tokens,
    request_wal_hot.prompt_tokens,
    request_wal_hot.created_at,
    request_wal_hot.completed_at,
    request_wal_hot.upstream_request_at,
    request_wal_hot.upstream_response_at,
    request_wal_hot.error,
    request_wal_hot.compression_strategy,
    request_wal_hot.compression_meta
   FROM public.request_wal_hot
UNION ALL
 SELECT request_wal.request_id,
    request_wal.tenant_id,
    request_wal.gw_session_id,
    request_wal.status,
    request_wal.stage,
    request_wal.client_model,
    request_wal.upstream_provider_id,
    request_wal.upstream_credential_id,
    request_wal.completion_tokens,
    request_wal.prompt_tokens,
    request_wal.created_at,
    request_wal.completed_at,
    request_wal.upstream_request_at,
    request_wal.upstream_response_at,
    request_wal.error,
    request_wal.compression_strategy,
    request_wal.compression_meta
   FROM public.request_wal;

