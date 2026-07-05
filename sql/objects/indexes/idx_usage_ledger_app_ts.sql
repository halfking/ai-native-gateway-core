--
-- Name: idx_usage_ledger_app_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_app_ts ON public.usage_ledger USING btree (application_id, ts DESC) WHERE (application_id IS NOT NULL);

