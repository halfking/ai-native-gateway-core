--
-- Name: runtime_metrics id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_metrics ALTER COLUMN id SET DEFAULT nextval('public.runtime_metrics_id_seq'::regclass);

