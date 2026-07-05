--
-- Name: maas_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maas_settings (
    id integer DEFAULT 1 NOT NULL,
    cents_per_credit numeric(10,4) DEFAULT 0.1 NOT NULL,
    base_credits_per_1m bigint DEFAULT 10000 NOT NULL,
    currency_display character varying(8) DEFAULT 'CNY'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    alipay_account character varying(128) DEFAULT ''::character varying NOT NULL,
    wechat_mch_id character varying(128) DEFAULT ''::character varying NOT NULL,
    stub_alipay_qr_url text DEFAULT ''::text NOT NULL,
    stub_wechat_qr_url text DEFAULT ''::text NOT NULL,
    base_credits_per_1m_out bigint,
    base_credits_per_1m_cache_in bigint,
    base_credits_per_1m_cache_out bigint,
    global_discount numeric(6,4) DEFAULT 1.0 NOT NULL,
    CONSTRAINT maas_settings_id_check CHECK ((id = 1))
);

