--
-- Name: provider_error_details id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_error_details ALTER COLUMN id SET DEFAULT nextval('public.provider_error_details_id_seq'::regclass);

