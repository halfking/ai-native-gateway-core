--
-- Name: runtime_telemetry_consent_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_telemetry_consent_events ALTER COLUMN id SET DEFAULT nextval('public.runtime_telemetry_consent_events_id_seq'::regclass);

