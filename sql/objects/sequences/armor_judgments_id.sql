--
-- Name: armor_judgments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.armor_judgments ALTER COLUMN id SET DEFAULT nextval('public.armor_judgments_id_seq'::regclass);

