--
-- Name: key_applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.key_applications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    client_ip inet NOT NULL,
    fingerprint text NOT NULL,
    contact text NOT NULL,
    purpose text,
    status text DEFAULT 'pending'::text NOT NULL,
    issued_key_id bigint,
    admin_notes text,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    expires_at timestamp with time zone DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT key_applications_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text])))
);

