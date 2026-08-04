--
-- Name: model_integrity_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_integrity_events ALTER COLUMN id SET DEFAULT nextval('public.model_integrity_events_id_seq'::regclass);

