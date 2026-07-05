--
-- Name: price_change_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.price_change_events ALTER COLUMN id SET DEFAULT nextval('public.price_change_events_id_seq'::regclass);

