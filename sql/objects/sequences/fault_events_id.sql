--
-- Name: fault_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fault_events ALTER COLUMN id SET DEFAULT nextval('public.fault_events_id_seq'::regclass);

