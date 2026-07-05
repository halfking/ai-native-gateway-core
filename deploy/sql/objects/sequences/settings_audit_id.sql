--
-- Name: settings_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_audit ALTER COLUMN id SET DEFAULT nextval('public.settings_audit_id_seq'::regclass);

