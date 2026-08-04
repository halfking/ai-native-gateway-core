--
-- Name: node_probe_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.node_probe_runs ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.node_probe_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

