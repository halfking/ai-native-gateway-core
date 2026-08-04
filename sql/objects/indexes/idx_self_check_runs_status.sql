--
-- Name: idx_self_check_runs_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_self_check_runs_status ON public.self_check_runs USING btree (status);

