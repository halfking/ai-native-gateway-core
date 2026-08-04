--
-- Name: vibe_code_reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.vibe_code_reviews (
    id bigint NOT NULL,
    session_id bigint,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    file_path text,
    language text,
    original_code text,
    review_result jsonb,
    score numeric(3,2),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

