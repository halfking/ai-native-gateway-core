--
-- Name: request_attachments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_attachments ALTER COLUMN id SET DEFAULT nextval('public.request_attachments_id_seq'::regclass);

