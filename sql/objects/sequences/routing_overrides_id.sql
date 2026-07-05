--
-- Name: routing_overrides id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides ALTER COLUMN id SET DEFAULT nextval('public.routing_overrides_id_seq'::regclass);

