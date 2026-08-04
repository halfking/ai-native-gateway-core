--
-- Name: idx_download_events_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_download_events_created ON public.download_events USING btree (created_at DESC);

