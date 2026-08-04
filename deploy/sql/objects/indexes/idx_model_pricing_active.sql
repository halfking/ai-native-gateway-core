--
-- Name: idx_model_pricing_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_pricing_active ON public.model_pricing USING btree (active) WHERE (active = true);

