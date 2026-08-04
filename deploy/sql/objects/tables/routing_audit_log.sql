--
-- Name: routing_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target_type text,
    target_id bigint,
    before_json jsonb,
    after_json jsonb,
    incident_id uuid,
    tenant_id text,
    confirmation_token_hash text,
    idempotency_key text,
    request_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    pre_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    post_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    response_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    outcome text,
    failure_reason text,
    diagnostic_run_id uuid,
    actor_ip_hash text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT routing_audit_log_action_check CHECK (((action IS NOT NULL) AND (length(action) > 0)))
);


--
-- Name: TABLE routing_audit_log; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.routing_audit_log IS 'Phase-2 append-only audit trail. Every mutating action and evidence export is recorded with the authenticated actor, the confirmation-token hash, an idempotency key, and a before/after snapshot. Rows are immutable once committed; the unique index on idempotency_key guarantees that operator retries do not double-execute.';

