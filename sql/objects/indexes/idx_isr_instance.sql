--
-- Name: idx_isr_instance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_isr_instance ON public.instance_status_reports USING btree (instance_id, "timestamp" DESC);

