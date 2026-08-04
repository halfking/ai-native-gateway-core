--
-- Name: injection_attack_vectors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.injection_attack_vectors (
    id bigint NOT NULL,
    tenant_id character varying(255) DEFAULT 'default'::character varying NOT NULL,
    attack_text text NOT NULL,
    attack_hash character varying(64) NOT NULL,
    categories public.injection_category[],
    severity integer,
    embedding text,
    source character varying(50) DEFAULT 'detection'::character varying NOT NULL,
    request_id character varying(255),
    detected_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    CONSTRAINT injection_attack_vectors_severity_check CHECK (((severity >= 1) AND (severity <= 10)))
);


--
-- Name: TABLE injection_attack_vectors; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.injection_attack_vectors IS '攻击向量库 - 存储历史攻击样本用于相似度检测';

