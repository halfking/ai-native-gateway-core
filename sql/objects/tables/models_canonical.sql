--
-- Name: models_canonical; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.models_canonical (
    id bigint NOT NULL,
    canonical_name text NOT NULL,
    family text,
    parameters_b numeric(8,2),
    modality text DEFAULT 'text'::text NOT NULL,
    context_window integer,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    tags_locked boolean DEFAULT false NOT NULL,
    tags_updated_at timestamp with time zone,
    display_name text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    disabled_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    input_price_cny numeric(10,4) DEFAULT 0,
    output_price_cny numeric(10,4) DEFAULT 0,
    released_at date,
    strengths text[] DEFAULT '{}'::text[] NOT NULL,
    cost_tier text DEFAULT 'unknown'::text NOT NULL,
    multimodal_caps text[] DEFAULT '{}'::text[] NOT NULL,
    version_rank integer,
    complexity_ceiling text,
    min_complexity text,
    CONSTRAINT models_canonical_complexity_ceiling_check CHECK (((complexity_ceiling IS NULL) OR (complexity_ceiling = ANY (ARRAY['easy'::text, 'medium'::text, 'hard'::text, 'frontier'::text])))),
    CONSTRAINT models_canonical_cost_tier_check CHECK ((cost_tier = ANY (ARRAY['free'::text, 'low'::text, 'medium'::text, 'high'::text, 'premium'::text, 'unknown'::text]))),
    CONSTRAINT models_canonical_min_complexity_check CHECK (((min_complexity IS NULL) OR (min_complexity = ANY (ARRAY['easy'::text, 'medium'::text, 'hard'::text, 'frontier'::text])))),
    CONSTRAINT models_canonical_modality_check CHECK ((modality = ANY (ARRAY['text'::text, 'vision'::text, 'audio'::text, 'video'::text, 'multimodal'::text, 'embedding'::text]))),
    CONSTRAINT models_canonical_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);

