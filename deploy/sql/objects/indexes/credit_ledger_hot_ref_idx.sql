--
-- Name: credit_ledger_hot_ref_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_hot_ref_idx ON public.credit_ledger_hot USING btree (ref_type, ref_id) WHERE (ref_type IS NOT NULL);

