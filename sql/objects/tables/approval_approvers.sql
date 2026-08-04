--
-- Name: approval_approvers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_approvers (
    id integer NOT NULL,
    tenant_id character varying(64) NOT NULL,
    user_id character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    email character varying(128),
    phone character varying(32),
    role character varying(32) NOT NULL,
    priority integer DEFAULT 0,
    enabled boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT approval_approvers_role_check CHECK (((role)::text = ANY (ARRAY[('admin'::character varying)::text, ('auditor'::character varying)::text, ('manager'::character varying)::text, ('reviewer'::character varying)::text])))
);

