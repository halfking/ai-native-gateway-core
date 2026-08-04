--
-- Name: credit_ledger_hot_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_hot_tenant_id_created_at_idx ON public.credit_ledger_hot USING btree (tenant_id, created_at);

