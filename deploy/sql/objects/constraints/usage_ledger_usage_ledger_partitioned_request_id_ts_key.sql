--
-- Name: usage_ledger usage_ledger_partitioned_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger
    ADD CONSTRAINT usage_ledger_partitioned_request_id_ts_key UNIQUE (request_id, ts);

