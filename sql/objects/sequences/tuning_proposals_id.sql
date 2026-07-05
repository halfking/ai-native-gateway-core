--
-- Name: tuning_proposals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_proposals ALTER COLUMN id SET DEFAULT nextval('public.tuning_proposals_id_seq'::regclass);

