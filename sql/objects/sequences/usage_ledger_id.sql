--
-- Name: usage_ledger id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ALTER COLUMN id SET DEFAULT nextval('public.usage_ledger_id_seq'::regclass);

