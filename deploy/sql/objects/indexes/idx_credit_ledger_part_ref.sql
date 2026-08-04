--
-- Name: idx_credit_ledger_part_ref; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_ref ON ONLY public.credit_ledger USING btree (ref_type, ref_id);

