--
-- Name: vcr_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX vcr_tenant ON public.vibe_code_reviews USING btree (tenant_id, created_at DESC);

