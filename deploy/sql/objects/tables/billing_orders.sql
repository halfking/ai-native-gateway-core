--
-- Name: billing_orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.billing_orders (
    id bigint NOT NULL,
    order_no character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    order_type character varying(16) NOT NULL,
    status character varying(16) DEFAULT 'pending'::character varying NOT NULL,
    amount_cents integer NOT NULL,
    credits bigint NOT NULL,
    plan_id integer,
    package_id integer,
    payment_channel character varying(16) DEFAULT 'alipay'::character varying NOT NULL,
    qr_payload text DEFAULT ''::text NOT NULL,
    qr_url text DEFAULT ''::text NOT NULL,
    paid_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT billing_orders_order_type_check CHECK (((order_type)::text = ANY (ARRAY[('subscribe'::character varying)::text, ('topup'::character varying)::text]))),
    CONSTRAINT billing_orders_payment_channel_check CHECK (((payment_channel)::text = ANY (ARRAY[('alipay'::character varying)::text, ('wechat'::character varying)::text, ('manual'::character varying)::text]))),
    CONSTRAINT billing_orders_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('paid'::character varying)::text, ('cancelled'::character varying)::text, ('expired'::character varying)::text])))
);

