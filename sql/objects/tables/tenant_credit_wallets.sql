--
-- Name: tenant_credit_wallets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_credit_wallets (
    tenant_id character varying(64) NOT NULL,
    balance_credits bigint DEFAULT 0 NOT NULL,
    locked_credits bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    granted_balance bigint DEFAULT 0 NOT NULL,
    purchased_balance bigint DEFAULT 0 NOT NULL
);

