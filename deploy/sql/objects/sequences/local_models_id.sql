--
-- Name: local_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models ALTER COLUMN id SET DEFAULT nextval('public.local_models_id_seq'::regclass);

