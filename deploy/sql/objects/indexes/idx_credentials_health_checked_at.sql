--
-- Name: idx_credentials_health_checked_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_health_checked_at ON public.credentials USING btree (tenant_id, health_checked_at DESC) WHERE (health_checked_at IS NOT NULL);

