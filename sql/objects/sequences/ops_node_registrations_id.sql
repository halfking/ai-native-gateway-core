--
-- Name: ops_node_registrations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_node_registrations ALTER COLUMN id SET DEFAULT nextval('public.ops_node_registrations_id_seq'::regclass);

