--
-- Name: tool_registry id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry ALTER COLUMN id SET DEFAULT nextval('public.tool_registry_id_seq'::regclass);

