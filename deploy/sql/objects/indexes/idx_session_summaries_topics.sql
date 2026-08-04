--
-- Name: idx_session_summaries_topics; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_topics ON public.session_summaries USING gin (key_topics);

