--
-- Name: routing_health_checks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.routing_health_checks ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.routing_health_checks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

