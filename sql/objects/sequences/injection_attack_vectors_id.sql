--
-- Name: injection_attack_vectors id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.injection_attack_vectors ALTER COLUMN id SET DEFAULT nextval('public.injection_attack_vectors_id_seq'::regclass);

