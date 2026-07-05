--
-- Name: idx_usage_ledger_application_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_application_id ON public.usage_ledger USING btree (application_id) WHERE (application_id IS NOT NULL);

