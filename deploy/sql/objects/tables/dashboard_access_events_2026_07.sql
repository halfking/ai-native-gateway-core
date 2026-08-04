--
-- Name: dashboard_access_events_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dashboard_access_events ATTACH PARTITION public.dashboard_access_events_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+08') TO ('2026-08-01 00:00:00+08');

