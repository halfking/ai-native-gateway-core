--
-- Name: idx_pricing_history_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_history_date ON public.model_pricing_history USING btree (effective_date DESC);

