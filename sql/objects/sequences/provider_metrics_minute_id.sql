--
-- Name: provider_metrics_minute id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_metrics_minute ALTER COLUMN id SET DEFAULT nextval('public.provider_metrics_minute_id_seq'::regclass);

