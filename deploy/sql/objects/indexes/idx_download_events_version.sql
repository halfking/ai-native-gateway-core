--
-- Name: idx_download_events_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_download_events_version ON public.download_events USING btree (release_version, created_at DESC);

