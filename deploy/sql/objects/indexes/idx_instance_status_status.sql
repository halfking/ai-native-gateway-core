--
-- Name: idx_instance_status_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_instance_status_status ON public.instance_release_status USING btree (status);

