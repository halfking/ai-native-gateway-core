--
-- Name: provider_scores id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_scores ALTER COLUMN id SET DEFAULT nextval('public.provider_scores_id_seq'::regclass);

