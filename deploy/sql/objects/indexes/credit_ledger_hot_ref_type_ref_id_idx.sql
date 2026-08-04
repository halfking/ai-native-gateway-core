--
-- Name: credit_ledger_hot_ref_type_ref_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_hot_ref_type_ref_id_idx ON public.credit_ledger_hot USING btree (ref_type, ref_id);

