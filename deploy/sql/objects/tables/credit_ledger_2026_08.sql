--
-- Name: credit_ledger_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ATTACH PARTITION public.credit_ledger_2026_08 FOR VALUES FROM ('2026-08-01 08:00:00+08') TO ('2026-09-01 08:00:00+08');

