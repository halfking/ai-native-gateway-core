--
-- Name: idx_usage_ledger_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_tenant_ts ON public.usage_ledger USING btree (tenant_id, ts DESC);

