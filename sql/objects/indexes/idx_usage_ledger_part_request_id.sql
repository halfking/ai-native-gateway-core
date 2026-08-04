--
-- Name: idx_usage_ledger_part_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_part_request_id ON ONLY public.usage_ledger USING btree (request_id);

