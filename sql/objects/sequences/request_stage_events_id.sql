--
-- Name: request_stage_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_stage_events ALTER COLUMN id SET DEFAULT nextval('public.request_stage_events_id_seq'::regclass);

