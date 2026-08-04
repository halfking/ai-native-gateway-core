--
-- Name: session_bodies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies ALTER COLUMN id SET DEFAULT nextval('public.session_bodies_id_seq'::regclass);

