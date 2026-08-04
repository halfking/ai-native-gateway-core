--
-- Name: provider_cost_reconciliation provider_cost_reconciliation_provider_id_reconciliation_mon_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_cost_reconciliation
    ADD CONSTRAINT provider_cost_reconciliation_provider_id_reconciliation_mon_key UNIQUE (provider_id, reconciliation_month);

