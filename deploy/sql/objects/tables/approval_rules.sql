--
-- Name: approval_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_rules (
    id integer NOT NULL,
    tenant_id character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    enabled boolean DEFAULT true,
    priority integer DEFAULT 0,
    conditions jsonb NOT NULL,
    action jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

