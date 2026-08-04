--
-- Name: compression_bench_results id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.compression_bench_results ALTER COLUMN id SET DEFAULT nextval('public.compression_bench_results_id_seq'::regclass);

