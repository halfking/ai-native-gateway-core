--
-- Name: provider_profile_alerts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_alerts ALTER COLUMN id SET DEFAULT nextval('public.provider_profile_alerts_id_seq'::regclass);

