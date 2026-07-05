--
-- Name: model_probe_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_runs ALTER COLUMN id SET DEFAULT nextval('public.model_probe_runs_id_seq'::regclass);

