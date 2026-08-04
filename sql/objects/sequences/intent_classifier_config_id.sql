--
-- Name: intent_classifier_config id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_classifier_config ALTER COLUMN id SET DEFAULT nextval('public.intent_classifier_config_id_seq'::regclass);

