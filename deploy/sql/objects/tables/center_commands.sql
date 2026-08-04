--
-- Name: center_commands; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.center_commands (
    id bigint NOT NULL,
    command_id text NOT NULL,
    instance_id text NOT NULL,
    command text NOT NULL,
    args jsonb,
    status text DEFAULT 'pending'::text NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    issued_by text NOT NULL,
    expires_at timestamp with time zone,
    executed_at timestamp with time zone,
    result jsonb,
    envelope_signature text,
    CONSTRAINT center_commands_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'executed'::text, 'failed'::text, 'expired'::text])))
);

