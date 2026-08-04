--
-- Name: idx_asset_rel_dst; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_rel_dst ON public.asset_relationships USING btree (dst_kind, dst_ref_id);

