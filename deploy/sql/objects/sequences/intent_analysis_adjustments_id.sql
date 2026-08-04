--
-- Name: intent_analysis_adjustments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_analysis_adjustments ALTER COLUMN id SET DEFAULT nextval('public.intent_analysis_adjustments_id_seq'::regclass);

