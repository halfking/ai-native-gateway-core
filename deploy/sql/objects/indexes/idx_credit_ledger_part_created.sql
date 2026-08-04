--
-- Name: idx_credit_ledger_part_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_created ON ONLY public.credit_ledger USING btree (created_at);

