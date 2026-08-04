--
-- Name: session_bodies_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies ATTACH PARTITION public.session_bodies_2026_08 FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

