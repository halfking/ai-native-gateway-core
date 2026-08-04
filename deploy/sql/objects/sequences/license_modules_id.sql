--
-- Name: license_modules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_modules ALTER COLUMN id SET DEFAULT nextval('public.license_modules_id_seq'::regclass);

