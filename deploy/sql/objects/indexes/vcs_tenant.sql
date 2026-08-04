--
-- Name: vcs_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX vcs_tenant ON public.vibe_coding_sessions USING btree (tenant_id, created_at DESC);

