--
-- Name: model_name_mapping id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_name_mapping ALTER COLUMN id SET DEFAULT nextval('public.model_name_mapping_id_seq'::regclass);

