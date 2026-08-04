--
-- Name: donations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.donations (
    id bigint NOT NULL,
    order_no text NOT NULL,
    holder_id bigint,
    email text DEFAULT ''::text NOT NULL,
    amount_cents integer NOT NULL,
    currency text DEFAULT 'CNY'::text NOT NULL,
    channel text DEFAULT 'alipay'::text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    tier_label text DEFAULT 'supporter'::text NOT NULL,
    paid_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT donations_amount_cents_check CHECK ((amount_cents > 0)),
    CONSTRAINT donations_channel_check CHECK ((channel = ANY (ARRAY['alipay'::text, 'wechat'::text, 'manual'::text]))),
    CONSTRAINT donations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'paid'::text, 'cancelled'::text, 'expired'::text])))
);

