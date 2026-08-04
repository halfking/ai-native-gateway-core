--
-- Name: security_detector_config id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_detector_config ALTER COLUMN id SET DEFAULT nextval('public.security_detector_config_id_seq'::regclass);

