--
-- Name: intent_classification_feedback id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_classification_feedback ALTER COLUMN id SET DEFAULT nextval('public.intent_classification_feedback_id_seq'::regclass);

