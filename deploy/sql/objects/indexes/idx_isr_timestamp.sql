--
-- Name: idx_isr_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_isr_timestamp ON public.instance_status_reports USING btree ("timestamp" DESC);

