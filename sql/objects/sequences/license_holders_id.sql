--
-- Name: license_holders id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_holders ALTER COLUMN id SET DEFAULT nextval('public.license_holders_id_seq'::regclass);

