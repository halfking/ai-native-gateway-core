--
-- Name: idx_security_config_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_config_tenant ON public.security_detector_config USING btree (tenant_id) WHERE (enabled = true);

