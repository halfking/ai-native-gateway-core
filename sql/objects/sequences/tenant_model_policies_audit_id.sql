--
-- Name: tenant_model_policies_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies_audit ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_audit_id_seq'::regclass);

