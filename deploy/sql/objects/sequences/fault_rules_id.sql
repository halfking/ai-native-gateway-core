--
-- Name: fault_rules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fault_rules ALTER COLUMN id SET DEFAULT nextval('public.fault_rules_id_seq'::regclass);

