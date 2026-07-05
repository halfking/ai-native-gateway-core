--
-- Name: idx_credentials_api_models_checked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_api_models_checked ON public.credentials USING btree (api_models_last_checked_at) WHERE (api_models_last_checked_at IS NOT NULL);

