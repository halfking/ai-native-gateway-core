--
-- Name: idx_session_summaries_cost; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_cost ON public.session_summaries USING btree (tenant_id, total_cost_usd DESC);

