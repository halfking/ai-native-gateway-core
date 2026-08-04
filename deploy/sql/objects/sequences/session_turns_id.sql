--
-- Name: session_turns id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns ALTER COLUMN id SET DEFAULT nextval('public.session_turns_id_seq'::regclass);

