--
-- Name: fault_action_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fault_action_logs ALTER COLUMN id SET DEFAULT nextval('public.fault_action_logs_id_seq'::regclass);

