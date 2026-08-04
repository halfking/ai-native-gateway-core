--
-- Name: severity_action_matrix id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.severity_action_matrix ALTER COLUMN id SET DEFAULT nextval('public.severity_action_matrix_id_seq'::regclass);

