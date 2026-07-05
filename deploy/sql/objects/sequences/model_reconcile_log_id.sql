--
-- Name: model_reconcile_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_reconcile_log ALTER COLUMN id SET DEFAULT nextval('public.model_reconcile_log_id_seq'::regclass);

