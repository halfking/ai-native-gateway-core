--
-- Name: model_aliases id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_aliases ALTER COLUMN id SET DEFAULT nextval('public.model_aliases_id_seq'::regclass);

