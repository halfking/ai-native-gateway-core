--
-- Name: usage_ledger_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.usage_ledger_with_current_month AS
 SELECT usage_ledger_hot.request_id,
    usage_ledger_hot.ts,
    usage_ledger_hot.tenant_id,
    usage_ledger_hot.application_id,
    usage_ledger_hot.api_key_id,
    usage_ledger_hot.end_user_id,
    usage_ledger_hot.credential_id,
    usage_ledger_hot.provider_id,
    usage_ledger_hot.canonical_id,
    usage_ledger_hot.raw_model_name,
    usage_ledger_hot.prompt_tokens,
    usage_ledger_hot.completion_tokens,
    usage_ledger_hot.cache_read_tokens,
    usage_ledger_hot.cache_write_tokens,
    usage_ledger_hot.total_tokens,
    usage_ledger_hot.cost_usd,
    usage_ledger_hot.latency_ms,
    usage_ledger_hot.success,
    usage_ledger_hot.error_kind
   FROM public.usage_ledger_hot
UNION ALL
 SELECT usage_ledger.request_id,
    usage_ledger.ts,
    usage_ledger.tenant_id,
    usage_ledger.application_id,
    usage_ledger.api_key_id,
    usage_ledger.end_user_id,
    usage_ledger.credential_id,
    usage_ledger.provider_id,
    usage_ledger.canonical_id,
    usage_ledger.raw_model_name,
    usage_ledger.prompt_tokens,
    usage_ledger.completion_tokens,
    usage_ledger.cache_read_tokens,
    usage_ledger.cache_write_tokens,
    usage_ledger.total_tokens,
    usage_ledger.cost_usd,
    usage_ledger.latency_ms,
    usage_ledger.success,
    usage_ledger.error_kind
   FROM public.usage_ledger;

