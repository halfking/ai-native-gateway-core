--
-- Name: vcr_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX vcr_session ON public.vibe_code_reviews USING btree (session_id);

