--
-- Name: ip_blocklist id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ip_blocklist ALTER COLUMN id SET DEFAULT nextval('public.ip_blocklist_id_seq'::regclass);

