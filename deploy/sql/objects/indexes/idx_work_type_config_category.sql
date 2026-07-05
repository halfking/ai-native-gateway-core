--
-- Name: idx_work_type_config_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_work_type_config_category ON public.work_type_config USING btree (category, sort_order);

