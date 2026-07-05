--
-- Name: model_families; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_families (
    id text NOT NULL,
    display_name text NOT NULL,
    vendor text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_families_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);

