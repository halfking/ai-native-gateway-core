--
-- Name: provider_health_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_health_events ALTER COLUMN id SET DEFAULT nextval('public.provider_health_events_id_seq'::regclass);

