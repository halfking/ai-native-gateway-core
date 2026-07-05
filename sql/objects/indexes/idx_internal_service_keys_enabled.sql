--
-- Name: idx_internal_service_keys_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_internal_service_keys_enabled ON public.internal_service_keys USING btree (enabled);

