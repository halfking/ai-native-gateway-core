--
-- Name: license_trial_consents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_trial_consents ALTER COLUMN id SET DEFAULT nextval('public.license_trial_consents_id_seq'::regclass);

