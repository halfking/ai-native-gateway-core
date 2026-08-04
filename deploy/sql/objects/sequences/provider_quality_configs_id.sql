--
-- Name: provider_quality_configs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_configs ALTER COLUMN id SET DEFAULT nextval('public.provider_quality_configs_id_seq'::regclass);

