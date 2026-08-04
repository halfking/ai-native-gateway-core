--
-- Name: canary_tokens id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.canary_tokens ALTER COLUMN id SET DEFAULT nextval('public.canary_tokens_id_seq'::regclass);

