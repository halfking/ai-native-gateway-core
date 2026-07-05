--
-- Name: tenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenants (
    code character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    status character varying(32) DEFAULT 'active'::character varying NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    contact_email character varying(256) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenants_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('trial'::character varying)::text, ('suspended'::character varying)::text, ('expired'::character varying)::text, ('disabled'::character varying)::text])))
);


SET default_table_access_method = columnar;
