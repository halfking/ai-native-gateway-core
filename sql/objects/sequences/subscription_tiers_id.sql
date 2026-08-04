--
-- Name: subscription_tiers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_tiers ALTER COLUMN id SET DEFAULT nextval('public.subscription_tiers_id_seq'::regclass);

