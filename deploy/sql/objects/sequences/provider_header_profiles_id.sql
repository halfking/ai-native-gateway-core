--
-- Name: provider_header_profiles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles ALTER COLUMN id SET DEFAULT nextval('public.provider_header_profiles_id_seq'::regclass);

