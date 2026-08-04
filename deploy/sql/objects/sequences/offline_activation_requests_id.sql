--
-- Name: offline_activation_requests id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.offline_activation_requests ALTER COLUMN id SET DEFAULT nextval('public.offline_activation_requests_id_seq'::regclass);

