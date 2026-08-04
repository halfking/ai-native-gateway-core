--
-- Name: vibe_coding_sessions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vibe_coding_sessions ALTER COLUMN id SET DEFAULT nextval('public.vibe_coding_sessions_id_seq'::regclass);

