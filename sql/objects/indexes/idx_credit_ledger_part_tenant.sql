--
-- Name: idx_credit_ledger_part_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_tenant ON ONLY public.credit_ledger USING btree (tenant_id, created_at);

