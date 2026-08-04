--
-- Name: ip_blocklist; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ip_blocklist (
    id bigint NOT NULL,
    ip_or_cidr text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    scope text DEFAULT 'global'::text NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    expires_at timestamp with time zone,
    hit_count bigint DEFAULT 0 NOT NULL,
    created_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ip_blocklist_scope_check CHECK ((scope = ANY (ARRAY['global'::text, 'collect'::text, 'ops'::text]))),
    CONSTRAINT ip_blocklist_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'auto_attack'::text])))
);

