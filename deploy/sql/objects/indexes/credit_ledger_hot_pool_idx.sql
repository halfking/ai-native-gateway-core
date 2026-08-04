--
-- Name: credit_ledger_hot_pool_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_hot_pool_idx ON public.credit_ledger_hot USING btree (pool, tenant_id) WHERE (pool IS NOT NULL);

