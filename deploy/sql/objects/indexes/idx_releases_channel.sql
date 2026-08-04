--
-- Name: idx_releases_channel; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_releases_channel ON public.releases USING btree (channel, build_seq DESC);

