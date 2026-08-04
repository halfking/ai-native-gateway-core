--
-- Name: release_artifacts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.release_artifacts ALTER COLUMN id SET DEFAULT nextval('public.release_artifacts_id_seq'::regclass);

