--
-- Name: price_change_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.price_change_events (
    id bigint NOT NULL,
    old_plan_id bigint,
    new_plan_id bigint NOT NULL,
    delta_json jsonb,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    notify_channel text,
    applied boolean DEFAULT false NOT NULL
);

