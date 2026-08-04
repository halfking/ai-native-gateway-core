--
-- Name: session_turn_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turn_logs ALTER COLUMN id SET DEFAULT nextval('public.session_turn_logs_id_seq'::regclass);

