--
-- Name: provider_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models ALTER COLUMN id SET DEFAULT nextval('public.provider_models_id_seq'::regclass);

