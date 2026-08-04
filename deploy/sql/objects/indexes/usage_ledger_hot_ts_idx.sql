--
-- Name: usage_ledger_hot_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_hot_ts_idx ON public.usage_ledger_hot USING btree (ts);

