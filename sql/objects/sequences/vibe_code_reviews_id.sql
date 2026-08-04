--
-- Name: vibe_code_reviews id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.vibe_code_reviews ALTER COLUMN id SET DEFAULT nextval('public.vibe_code_reviews_id_seq'::regclass);

