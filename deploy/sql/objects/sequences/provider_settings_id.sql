--
-- Name: provider_settings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings ALTER COLUMN id SET DEFAULT nextval('public.provider_settings_id_seq'::regclass);

