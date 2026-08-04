--
-- Name: gray_release_rules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gray_release_rules ALTER COLUMN id SET DEFAULT nextval('public.gray_release_rules_id_seq'::regclass);

