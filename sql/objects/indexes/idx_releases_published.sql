--
-- Name: idx_releases_published; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_releases_published ON public.releases USING btree (published_at DESC) WHERE (published_at IS NOT NULL);

