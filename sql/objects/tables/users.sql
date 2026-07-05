--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    username character varying(128) NOT NULL,
    password_hash character varying(256) NOT NULL,
    display_name character varying(128) DEFAULT ''::character varying NOT NULL,
    email character varying(256) DEFAULT ''::character varying NOT NULL,
    role character varying(32) DEFAULT 'tenant_admin'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

