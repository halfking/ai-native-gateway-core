--
-- Name: toxic_keywords id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.toxic_keywords ALTER COLUMN id SET DEFAULT nextval('public.toxic_keywords_id_seq'::regclass);

