--
-- Name: center_commands id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.center_commands ALTER COLUMN id SET DEFAULT nextval('public.center_commands_id_seq'::regclass);

