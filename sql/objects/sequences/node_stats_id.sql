--
-- Name: node_stats id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.node_stats ALTER COLUMN id SET DEFAULT nextval('public.node_stats_id_seq'::regclass);

