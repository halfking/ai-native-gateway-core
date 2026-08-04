--
-- Name: idx_quality_profiles_calculated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_quality_profiles_calculated_at ON public.provider_quality_profiles USING btree (calculated_at DESC);

