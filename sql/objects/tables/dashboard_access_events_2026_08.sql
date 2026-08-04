--
-- Name: dashboard_access_events_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dashboard_access_events ATTACH PARTITION public.dashboard_access_events_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08');

