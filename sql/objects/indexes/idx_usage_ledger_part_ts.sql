--
-- Name: idx_usage_ledger_part_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_part_ts ON ONLY public.usage_ledger USING btree (ts);

