--
-- Name: session_intent_evolution id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_intent_evolution ALTER COLUMN id SET DEFAULT nextval('public.session_intent_evolution_id_seq'::regclass);

