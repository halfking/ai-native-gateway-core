--
-- Name: request_logs_bodies_2026_09; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_bodies ATTACH PARTITION public.request_logs_bodies_2026_09 FOR VALUES FROM ('2026-09-01 00:00:00+08') TO ('2026-10-01 00:00:00+08');

