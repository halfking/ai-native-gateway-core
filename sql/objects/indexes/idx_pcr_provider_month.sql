--
-- Name: idx_pcr_provider_month; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pcr_provider_month ON public.provider_cost_reconciliation USING btree (provider_id, reconciliation_month DESC);

