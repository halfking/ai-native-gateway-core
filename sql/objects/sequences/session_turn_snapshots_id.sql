--
-- Name: session_turn_snapshots id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turn_snapshots ALTER COLUMN id SET DEFAULT nextval('public.session_turn_snapshots_id_seq'::regclass);

