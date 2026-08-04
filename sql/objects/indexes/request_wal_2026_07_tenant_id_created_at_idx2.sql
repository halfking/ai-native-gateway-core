--
-- Name: request_wal_2026_07_tenant_id_created_at_idx2; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_tenant_id_created_at_idx2 ON public.request_wal_2026_07 USING btree (tenant_id, created_at DESC);

