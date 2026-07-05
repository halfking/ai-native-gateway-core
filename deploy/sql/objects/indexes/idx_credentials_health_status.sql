--
-- Name: idx_credentials_health_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_health_status ON public.credentials USING btree (tenant_id, health_status);

