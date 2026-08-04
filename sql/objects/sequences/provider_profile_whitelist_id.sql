--
-- Name: provider_profile_whitelist id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_profile_whitelist ALTER COLUMN id SET DEFAULT nextval('public.provider_profile_whitelist_id_seq'::regclass);

