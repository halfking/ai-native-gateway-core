--
-- Name: pricing_refresh_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_refresh_log ALTER COLUMN id SET DEFAULT nextval('public.pricing_refresh_log_id_seq'::regclass);

