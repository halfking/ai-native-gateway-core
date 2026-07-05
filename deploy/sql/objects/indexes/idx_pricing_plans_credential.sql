--
-- Name: idx_pricing_plans_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_credential ON public.pricing_plans USING btree (credential_id) WHERE (credential_id IS NOT NULL);

