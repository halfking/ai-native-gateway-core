--
-- Name: provider_profile_metrics id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_metrics ALTER COLUMN id SET DEFAULT nextval('public.provider_profile_metrics_id_seq'::regclass);

