--
-- Name: candidate_failure_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.candidate_failure_logs ALTER COLUMN id SET DEFAULT nextval('public.candidate_failure_logs_id_seq'::regclass);

