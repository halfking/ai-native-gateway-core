--
-- Name: task_default_routing_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.task_default_routing_audit ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.task_default_routing_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

