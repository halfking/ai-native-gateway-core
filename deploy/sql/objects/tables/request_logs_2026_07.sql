--
-- Name: request_logs_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

