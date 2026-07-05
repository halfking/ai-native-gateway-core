--
-- Name: topup_packages id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages ALTER COLUMN id SET DEFAULT nextval('public.topup_packages_id_seq'::regclass);

