--
-- Name: idx_ped_last_seen; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ped_last_seen ON public.provider_error_details USING btree (last_seen_at DESC);

