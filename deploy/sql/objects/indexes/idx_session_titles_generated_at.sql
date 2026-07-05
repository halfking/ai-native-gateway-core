--
-- Name: idx_session_titles_generated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_titles_generated_at ON public.session_titles USING btree (generated_at DESC);

