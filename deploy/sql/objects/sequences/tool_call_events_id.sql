--
-- Name: tool_call_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_call_events ALTER COLUMN id SET DEFAULT nextval('public.tool_call_events_id_seq'::regclass);

