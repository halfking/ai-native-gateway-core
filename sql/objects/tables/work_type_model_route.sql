--
-- Name: work_type_model_route; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_type_model_route (
    id integer NOT NULL,
    work_type_key text NOT NULL,
    canonical_name text NOT NULL,
    weight numeric(5,2) DEFAULT 1.0 NOT NULL,
    min_score numeric(8,4) DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL
);

