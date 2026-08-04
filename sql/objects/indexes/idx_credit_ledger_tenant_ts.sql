--
-- Name: idx_credit_ledger_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_tenant_ts ON public.credit_ledger_old USING btree (tenant_id, created_at DESC);

