--
-- Name: gray_release_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gray_release_rules (
    id bigint NOT NULL,
    release_id bigint NOT NULL,
    phase text NOT NULL,
    percent integer NOT NULL,
    selectors jsonb,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gray_release_rules_percent_check CHECK (((percent >= 0) AND (percent <= 100))),
    CONSTRAINT gray_release_rules_phase_check CHECK ((phase = ANY (ARRAY['canary'::text, 'batch_1'::text, 'batch_2'::text, 'batch_3'::text, 'full'::text]))),
    CONSTRAINT gray_release_rules_status_check CHECK ((status = ANY (ARRAY['active'::text, 'paused'::text, 'completed'::text])))
);

