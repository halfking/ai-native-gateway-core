--
-- Name: idx_self_check_rounds_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_self_check_rounds_run ON public.self_check_round_results USING btree (run_id);

