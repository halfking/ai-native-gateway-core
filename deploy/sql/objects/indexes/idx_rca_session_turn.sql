--
-- Name: idx_rca_session_turn; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_session_turn ON public.request_context_attrs USING btree (gw_session_id, turn_no);

