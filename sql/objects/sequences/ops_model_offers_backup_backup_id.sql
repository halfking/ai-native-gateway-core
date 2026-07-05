--
-- Name: ops_model_offers_backup backup_id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_model_offers_backup ALTER COLUMN backup_id SET DEFAULT nextval('public.ops_model_offers_backup_backup_id_seq'::regclass);

