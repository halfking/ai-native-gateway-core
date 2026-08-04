--
-- Name: idx_fr_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fr_enabled ON public.fault_rules USING btree (enabled);

