--
-- Name: credentials id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials ALTER COLUMN id SET DEFAULT nextval('public.credentials_id_seq'::regclass);

