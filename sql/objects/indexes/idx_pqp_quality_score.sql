--
-- Name: idx_pqp_quality_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pqp_quality_score ON public.provider_quality_profiles USING btree (quality_score DESC);

