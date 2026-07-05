--
-- Name: tuning_proposals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_proposals (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    category text NOT NULL,
    task_type text,
    proposal jsonb NOT NULL,
    evidence jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    applied_at timestamp with time zone,
    review_note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tuning_proposals_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'applied'::text, 'expired'::text])))
);

