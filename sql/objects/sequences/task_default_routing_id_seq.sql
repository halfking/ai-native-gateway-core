--
-- Name: task_default_routing_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.task_default_routing ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.task_default_routing_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

