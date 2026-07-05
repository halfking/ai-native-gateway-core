--
-- Name: idx_usage_ledger_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_ts ON public.usage_ledger USING btree (ts DESC);

