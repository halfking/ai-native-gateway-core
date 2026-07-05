--
-- Name: session_memora_extraction_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_memora_extraction_log (
    task_id text NOT NULL,
    extracted_at timestamp with time zone DEFAULT now() NOT NULL,
    written integer DEFAULT 0 NOT NULL,
    skipped_noise integer DEFAULT 0 NOT NULL,
    skipped_duplicate integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'ok'::text NOT NULL,
    detail jsonb
);

