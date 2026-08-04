--
-- Name: usage_ledger_hot_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_hot_api_key_id_ts_idx ON public.usage_ledger_hot USING btree (api_key_id, ts DESC) WHERE (api_key_id IS NOT NULL);

