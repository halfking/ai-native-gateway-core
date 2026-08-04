--
-- Name: request_logs_bodies_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_bodies ATTACH PARTITION public.request_logs_bodies_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08');

