--
-- Name: prompt_injection_llm_engines id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_llm_engines ALTER COLUMN id SET DEFAULT nextval('public.prompt_injection_llm_engines_id_seq'::regclass);

