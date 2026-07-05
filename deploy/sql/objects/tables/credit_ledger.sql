--
-- Name: credit_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    entry_type character varying(32) NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying(32),
    ref_id character varying(128),
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying(32),
    CONSTRAINT credit_ledger_entry_type_check CHECK (((entry_type)::text = ANY (ARRAY[('consume'::character varying)::text, ('topup'::character varying)::text, ('subscribe'::character varying)::text, ('adjust'::character varying)::text, ('refund'::character varying)::text])))
);

