--
-- Name: fault_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fault_rules (
    id integer NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    metric text NOT NULL,
    operator text NOT NULL,
    threshold double precision NOT NULL,
    duration text NOT NULL,
    severity text NOT NULL,
    action text NOT NULL,
    action_config jsonb,
    enabled boolean DEFAULT true NOT NULL,
    cooldown text DEFAULT '5m'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fault_rules_operator_check CHECK ((operator = ANY (ARRAY['gte'::text, 'lte'::text, 'eq'::text, 'ne'::text]))),
    CONSTRAINT fault_rules_severity_check CHECK ((severity = ANY (ARRAY['info'::text, 'warning'::text, 'error'::text, 'critical'::text])))
);

