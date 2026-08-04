--
-- Name: download_publish_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.download_publish_runs ALTER COLUMN id SET DEFAULT nextval('public.download_publish_runs_id_seq'::regclass);

