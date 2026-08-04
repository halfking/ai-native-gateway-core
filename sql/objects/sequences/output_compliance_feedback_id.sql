--
-- Name: output_compliance_feedback id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.output_compliance_feedback ALTER COLUMN id SET DEFAULT nextval('public.output_compliance_feedback_id_seq'::regclass);

