--
-- Name: idx_fr_metric; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fr_metric ON public.fault_rules USING btree (metric);

