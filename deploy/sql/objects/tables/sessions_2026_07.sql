--
-- Name: sessions_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions ATTACH PARTITION public.sessions_2026_07 FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

