--
-- Name: output_compliance_review_queue id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.output_compliance_review_queue ALTER COLUMN id SET DEFAULT nextval('public.output_compliance_review_queue_id_seq'::regclass);

