--
-- Name: idx_pricing_plans_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_provider ON public.pricing_plans USING btree (provider_id) WHERE (provider_id IS NOT NULL);

