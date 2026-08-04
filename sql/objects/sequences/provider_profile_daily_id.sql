--
-- Name: provider_profile_daily id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_daily ALTER COLUMN id SET DEFAULT nextval('public.provider_profile_daily_id_seq'::regclass);

