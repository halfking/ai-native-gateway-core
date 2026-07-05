--
-- Name: tenant_model_policies_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_model_policies_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    policy_id bigint,
    tenant_id text,
    canonical_name text,
    reason text,
    actor text,
    CONSTRAINT tenant_model_policies_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text, 'undelete'::text])))
);

ALTER TABLE ONLY public.tenant_model_policies_audit FORCE ROW LEVEL SECURITY;

