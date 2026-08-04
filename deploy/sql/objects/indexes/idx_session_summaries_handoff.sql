--
-- Name: idx_session_summaries_handoff; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_handoff ON public.session_summaries USING btree (last_handoff_at) WHERE (handoff_count > 0);

