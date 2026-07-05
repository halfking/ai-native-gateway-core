--
-- Name: idx_usage_ledger_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_credential_ts ON public.usage_ledger USING btree (credential_id, ts DESC);

