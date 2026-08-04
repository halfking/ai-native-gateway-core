--
-- Name: session_module_executions_hot execution_id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_module_executions_hot ALTER COLUMN execution_id SET DEFAULT nextval('public.session_module_executions_hot_execution_id_seq'::regclass);

