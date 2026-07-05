--
-- Name: auto_tune_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auto_tune_audit ALTER COLUMN id SET DEFAULT nextval('public.auto_tune_audit_id_seq'::regclass);

