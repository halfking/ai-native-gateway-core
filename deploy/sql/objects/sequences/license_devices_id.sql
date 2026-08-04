--
-- Name: license_devices id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_devices ALTER COLUMN id SET DEFAULT nextval('public.license_devices_id_seq'::regclass);

