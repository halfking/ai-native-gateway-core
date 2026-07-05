--
-- Name: model_offers_legacy id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy ALTER COLUMN id SET DEFAULT nextval('public.model_offers_id_seq'::regclass);

