--
-- Name: route_incident_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.route_incident_events ALTER COLUMN id SET DEFAULT nextval('public.route_incident_events_id_seq'::regclass);

