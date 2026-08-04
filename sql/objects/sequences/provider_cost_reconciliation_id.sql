--
-- Name: provider_cost_reconciliation id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_cost_reconciliation ALTER COLUMN id SET DEFAULT nextval('public.provider_cost_reconciliation_id_seq'::regclass);

