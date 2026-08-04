--
-- Name: idx_goal_sessions_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_goal_sessions_session ON public.goal_sessions USING btree (session_id);

