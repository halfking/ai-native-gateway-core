--
-- Name: api_key_auto_profile; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_key_auto_profile (
    api_key_id integer NOT NULL,
    profile text DEFAULT 'smart'::text NOT NULL,
    first_chosen_at timestamp with time zone DEFAULT now(),
    last_used_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT api_key_auto_profile_profile_check CHECK ((profile = ANY (ARRAY['smart'::text, 'speed_first'::text, 'cost_first'::text])))
);


--
-- Name: TABLE api_key_auto_profile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.api_key_auto_profile IS 'Auto route: per-API-Key profile preference (sticky 30min)';

