--
-- Name: credential_probes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probes ALTER COLUMN id SET DEFAULT nextval('public.credential_probes_id_seq'::regclass);

