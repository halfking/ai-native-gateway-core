--
-- Name: idx_request_wal_hot_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_wal_hot_created_at ON public.request_wal_hot USING btree (created_at DESC);

