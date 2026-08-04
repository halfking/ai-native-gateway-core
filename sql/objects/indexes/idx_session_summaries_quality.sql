--
-- Name: idx_session_summaries_quality; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_summaries_quality ON public.session_summaries USING btree (quality_score DESC) WHERE (quality_score IS NOT NULL);

