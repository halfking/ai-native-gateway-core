--
-- Name: idx_pricing_plans_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_model ON public.pricing_plans USING btree (model_canonical_id) WHERE (model_canonical_id IS NOT NULL);

