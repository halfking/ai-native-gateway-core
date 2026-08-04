--
-- Name: idx_quality_profiles_quality_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_quality_profiles_quality_score ON public.provider_quality_profiles USING btree (quality_score DESC);

