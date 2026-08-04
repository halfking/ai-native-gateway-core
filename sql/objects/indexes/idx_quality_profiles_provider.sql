--
-- Name: idx_quality_profiles_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_quality_profiles_provider ON public.provider_quality_profiles USING btree (provider_id);

