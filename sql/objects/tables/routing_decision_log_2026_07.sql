--
-- Name: routing_decision_log_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_decision_log ATTACH PARTITION public.routing_decision_log_2026_07 FOR VALUES FROM ('2026-07-01 08:00:00+08') TO ('2026-08-01 08:00:00+08');

