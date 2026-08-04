--
-- Name: idx_request_wal_hot_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_wal_hot_tenant_created ON public.request_wal_hot USING btree (tenant_id, created_at DESC);

