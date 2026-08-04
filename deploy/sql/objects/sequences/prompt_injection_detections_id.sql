--
-- Name: prompt_injection_detections id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_detections ALTER COLUMN id SET DEFAULT nextval('public.prompt_injection_detections_id_seq'::regclass);

