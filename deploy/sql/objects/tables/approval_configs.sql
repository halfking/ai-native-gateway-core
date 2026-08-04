--
-- Name: approval_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_configs (
    id integer NOT NULL,
    tenant_id character varying(64) NOT NULL,
    enabled boolean DEFAULT false,
    mode character varying(32) DEFAULT 'disabled'::character varying NOT NULL,
    timeout_seconds integer DEFAULT 3600,
    auto_reject_on_timeout boolean DEFAULT true,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT approval_configs_mode_check CHECK (((mode)::text = ANY (ARRAY[('disabled'::character varying)::text, ('automatic'::character varying)::text, ('manual'::character varying)::text])))
);

