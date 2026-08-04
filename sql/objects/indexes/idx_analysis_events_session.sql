--
-- Name: idx_analysis_events_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_analysis_events_session ON public.analysis_events USING btree (session_id, occurred_at DESC) WHERE (session_id IS NOT NULL);

