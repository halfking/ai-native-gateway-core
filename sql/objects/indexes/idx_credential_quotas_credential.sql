--
-- Name: idx_credential_quotas_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_quotas_credential ON public.credential_quotas USING btree (credential_id) WHERE (enabled = true);

