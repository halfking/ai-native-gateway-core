--
-- Name: runtime_alert_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_alert_events ALTER COLUMN id SET DEFAULT nextval('public.runtime_alert_events_id_seq'::regclass);

