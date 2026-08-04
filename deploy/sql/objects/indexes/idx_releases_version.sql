--
-- Name: idx_releases_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_releases_version ON public.releases USING btree (version);

