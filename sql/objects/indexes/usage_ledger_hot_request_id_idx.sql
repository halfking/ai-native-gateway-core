--
-- Name: usage_ledger_hot_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_hot_request_id_idx ON public.usage_ledger_hot USING btree (request_id);

