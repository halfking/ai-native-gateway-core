--
-- Name: usage_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    requests bigint DEFAULT 0 NOT NULL,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    errors bigint DEFAULT 0 NOT NULL
);

