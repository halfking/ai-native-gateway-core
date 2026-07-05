--
-- Name: idx_bg_tasks_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_status ON public.background_tasks USING btree (status) WHERE (status = 'running'::text);

