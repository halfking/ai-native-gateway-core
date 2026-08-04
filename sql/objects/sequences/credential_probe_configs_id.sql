--
-- Name: credential_probe_configs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probe_configs ALTER COLUMN id SET DEFAULT nextval('public.credential_probe_configs_id_seq'::regclass);

