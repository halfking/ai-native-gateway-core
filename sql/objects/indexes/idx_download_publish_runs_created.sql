--
-- Name: idx_download_publish_runs_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_download_publish_runs_created ON public.download_publish_runs USING btree (created_at DESC);

