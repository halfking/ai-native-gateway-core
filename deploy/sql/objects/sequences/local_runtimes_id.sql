--
-- Name: local_runtimes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes ALTER COLUMN id SET DEFAULT nextval('public.local_runtimes_id_seq'::regclass);

