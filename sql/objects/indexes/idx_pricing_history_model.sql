--
-- Name: idx_pricing_history_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_history_model ON public.model_pricing_history USING btree (model_canonical);

