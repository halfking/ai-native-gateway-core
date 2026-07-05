--
-- Name: idx_credentials_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_provider ON public.credentials USING btree (provider_id);

