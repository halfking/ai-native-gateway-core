--
-- Name: provider_quality_profiles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_profiles ALTER COLUMN id SET DEFAULT nextval('public.provider_quality_profiles_id_seq'::regclass);

