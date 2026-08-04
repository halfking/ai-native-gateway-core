--
-- Name: routing_decision_log_archive_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_decision_log_archive ATTACH PARTITION public.routing_decision_log_archive_2026_08 FOR VALUES FROM ('2026-08-01 08:00:00+08') TO ('2026-09-01 08:00:00+08');

