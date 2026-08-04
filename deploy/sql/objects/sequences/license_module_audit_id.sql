--
-- Name: license_module_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_module_audit ALTER COLUMN id SET DEFAULT nextval('public.license_module_audit_id_seq'::regclass);

