--
-- Name: idx_ppw_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppw_provider ON public.provider_profile_whitelist USING btree (provider_id);

