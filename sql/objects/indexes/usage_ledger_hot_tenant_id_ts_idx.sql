--
-- Name: usage_ledger_hot_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_hot_tenant_id_ts_idx ON public.usage_ledger_hot USING btree (tenant_id, ts);

