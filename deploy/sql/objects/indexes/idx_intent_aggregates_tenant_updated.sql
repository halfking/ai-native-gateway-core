--
-- Name: idx_intent_aggregates_tenant_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_intent_aggregates_tenant_updated ON public.intent_aggregates USING btree (tenant_id, last_updated DESC);

