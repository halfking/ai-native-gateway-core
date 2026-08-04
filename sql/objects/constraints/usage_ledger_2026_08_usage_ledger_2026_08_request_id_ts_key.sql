--
-- Name: usage_ledger_2026_08 usage_ledger_2026_08_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_2026_08
    ADD CONSTRAINT usage_ledger_2026_08_request_id_ts_key UNIQUE (request_id, ts);

