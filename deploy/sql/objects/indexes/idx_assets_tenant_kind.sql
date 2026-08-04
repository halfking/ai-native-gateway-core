--
-- Name: idx_assets_tenant_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_assets_tenant_kind ON public.assets USING btree (tenant_id, kind);

