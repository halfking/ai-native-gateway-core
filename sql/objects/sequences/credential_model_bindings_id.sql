--
-- Name: credential_model_bindings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings ALTER COLUMN id SET DEFAULT nextval('public.credential_model_bindings_id_seq'::regclass);

