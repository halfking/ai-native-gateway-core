--
-- Name: provider_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_events ALTER COLUMN id SET DEFAULT nextval('public.provider_events_id_seq'::regclass);

