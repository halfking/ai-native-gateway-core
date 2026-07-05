--
-- Name: tenant_model_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_id_seq'::regclass);

