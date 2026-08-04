--
-- Name: analysis_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.analysis_events ALTER COLUMN id SET DEFAULT nextval('public.analysis_events_id_seq'::regclass);

