--
-- Name: models_canonical id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical ALTER COLUMN id SET DEFAULT nextval('public.models_canonical_id_seq'::regclass);

