--
-- Name: pii_patterns; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pii_patterns (
    id integer NOT NULL,
    pattern_name character varying(100) NOT NULL,
    pattern_type character varying(50) NOT NULL,
    regex_pattern text NOT NULL,
    description text,
    enabled boolean DEFAULT true,
    severity integer DEFAULT 7,
    redact_format character varying(100),
    created_at timestamp with time zone DEFAULT now()
);

