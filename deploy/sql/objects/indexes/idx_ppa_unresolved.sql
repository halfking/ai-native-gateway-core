--
-- Name: idx_ppa_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppa_unresolved ON public.provider_profile_alerts USING btree (resolved_at) WHERE (resolved_at IS NULL);

