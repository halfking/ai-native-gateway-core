--
-- Name: download_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.download_events ALTER COLUMN id SET DEFAULT nextval('public.download_events_id_seq'::regclass);

