--
-- Name: credit_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger (
    id bigint NOT NULL,
    tenant_id character varying NOT NULL,
    entry_type character varying NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying,
    ref_id character varying,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying
)
PARTITION BY RANGE (created_at);

