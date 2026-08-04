--
-- Name: usage_ledger_2026_07 usage_ledger_2026_07_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_2026_07
    ADD CONSTRAINT usage_ledger_2026_07_request_id_ts_key UNIQUE (request_id, ts);

