--
-- Name: idx_pricing_plans_effective_to_null; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_effective_to_null ON public.pricing_plans USING btree (effective_to) WHERE (effective_to IS NULL);

