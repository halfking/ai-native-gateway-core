--
-- Name: request_wal_hot_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_hot_tenant_id_created_at_idx ON public.request_wal_hot USING btree (tenant_id, created_at DESC);

