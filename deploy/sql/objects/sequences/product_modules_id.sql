--
-- Name: product_modules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_modules ALTER COLUMN id SET DEFAULT nextval('public.product_modules_id_seq'::regclass);

