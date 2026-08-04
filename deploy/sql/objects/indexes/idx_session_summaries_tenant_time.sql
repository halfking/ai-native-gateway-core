--
-- Name: idx_session_summaries_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_tenant_time ON public.session_summaries USING btree (tenant_id, last_request_at DESC);

