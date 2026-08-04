--
-- Name: approval_routing_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.approval_routing_rules (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    rule_name text NOT NULL,
    rule_type text NOT NULL,
    conditions jsonb DEFAULT '{}'::jsonb NOT NULL,
    approvers jsonb DEFAULT '[]'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    risk_level character varying(16),
    channel_type character varying(16),
    approver_ids jsonb DEFAULT '[]'::jsonb,
    priority integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_routing_risk_level CHECK (((risk_level IS NULL) OR ((risk_level)::text = ANY ((ARRAY['low'::character varying, 'medium'::character varying, 'high'::character varying, 'critical'::character varying])::text[]))))
);

