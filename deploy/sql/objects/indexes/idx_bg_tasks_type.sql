--
-- Name: idx_bg_tasks_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_type ON public.background_tasks USING btree (task_type, started_at DESC);

