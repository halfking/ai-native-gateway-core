--
-- Name: handoff_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.handoff_logs ALTER COLUMN id SET DEFAULT nextval('public.handoff_logs_id_seq'::regclass);

