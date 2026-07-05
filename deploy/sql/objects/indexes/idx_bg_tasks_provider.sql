--
-- Name: idx_bg_tasks_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_provider ON public.background_tasks USING btree (provider_id, started_at DESC);

