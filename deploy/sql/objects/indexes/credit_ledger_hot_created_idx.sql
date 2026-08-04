--
-- Name: credit_ledger_hot_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_hot_created_idx ON public.credit_ledger_hot USING btree (created_at DESC);

