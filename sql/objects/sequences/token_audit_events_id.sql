--
-- Name: token_audit_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_audit_events ALTER COLUMN id SET DEFAULT nextval('public.token_audit_events_id_seq'::regclass);

