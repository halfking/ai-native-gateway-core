--
-- Name: idx_security_config_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_config_version ON public.security_detector_config USING btree (version DESC, updated_at DESC);

