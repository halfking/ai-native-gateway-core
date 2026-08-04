--
-- Name: idx_intent_config_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_intent_config_tenant ON public.intent_classifier_config USING btree (tenant_id) WHERE (tenant_id IS NOT NULL);

