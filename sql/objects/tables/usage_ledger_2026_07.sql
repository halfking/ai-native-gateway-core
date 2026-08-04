--
-- Name: usage_ledger_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ATTACH PARTITION public.usage_ledger_2026_07 FOR VALUES FROM ('2026-07-01 08:00:00+08') TO ('2026-08-01 08:00:00+08');

