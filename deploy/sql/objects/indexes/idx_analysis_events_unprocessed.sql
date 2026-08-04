--
-- Name: idx_analysis_events_unprocessed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_analysis_events_unprocessed ON public.analysis_events USING btree (occurred_at) WHERE (processed_at IS NULL);

