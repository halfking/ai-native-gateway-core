--
-- Name: armor_judgments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.armor_judgments (
    id bigint NOT NULL,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    check_type text NOT NULL,
    decision text NOT NULL,
    source text NOT NULL,
    pattern_ids text[],
    judge_model text,
    score real,
    threshold real,
    mode text DEFAULT 'observe'::text NOT NULL,
    latency_ms integer DEFAULT 0 NOT NULL,
    prompt_sha256 text,
    snippet text,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_armor_check CHECK ((check_type = ANY (ARRAY['prompt_inject'::text, 'pii'::text, 'hallucination'::text]))),
    CONSTRAINT chk_armor_decision CHECK ((decision = ANY (ARRAY['safe'::text, 'warn'::text, 'block'::text]))),
    CONSTRAINT chk_armor_mode CHECK ((mode = ANY (ARRAY['observe'::text, 'enforce'::text]))),
    CONSTRAINT chk_armor_source CHECK ((source = ANY (ARRAY['pattern'::text, 'judge'::text])))
);

