--
-- Name: test_columnar_new; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.test_columnar_new (
    id integer NOT NULL,
    tenant_id text,
    model text,
    prompt_tokens integer,
    completion_tokens integer,
    created_at timestamp with time zone DEFAULT now()
);


SET default_table_access_method = heap;
