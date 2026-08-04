--
-- Name: intent_aggregates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.intent_aggregates (
    tenant_id text NOT NULL,
    intent_kind text NOT NULL,
    count bigint DEFAULT 0 NOT NULL,
    last_updated timestamp with time zone DEFAULT now() NOT NULL
);

