--
-- Name: credit_ledger_old id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger_old ALTER COLUMN id SET DEFAULT nextval('public.credit_ledger_id_seq'::regclass);

