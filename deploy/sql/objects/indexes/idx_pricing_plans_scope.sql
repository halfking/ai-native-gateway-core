--
-- Name: idx_pricing_plans_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_scope ON public.pricing_plans USING btree (scope);

