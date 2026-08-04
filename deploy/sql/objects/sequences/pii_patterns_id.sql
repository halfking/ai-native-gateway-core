--
-- Name: pii_patterns id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pii_patterns ALTER COLUMN id SET DEFAULT nextval('public.pii_patterns_id_seq'::regclass);

