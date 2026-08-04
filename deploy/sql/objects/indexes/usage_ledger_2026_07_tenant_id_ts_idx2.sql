--
-- Name: usage_ledger_2026_07_tenant_id_ts_idx2; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_07_tenant_id_ts_idx2 ON public.usage_ledger_2026_07 USING btree (tenant_id, ts);

