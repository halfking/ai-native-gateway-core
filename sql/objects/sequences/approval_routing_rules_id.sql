--
-- Name: approval_routing_rules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.approval_routing_rules ALTER COLUMN id SET DEFAULT nextval('public.approval_routing_rules_id_seq'::regclass);

