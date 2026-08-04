--
-- Name: idx_instance_status_release; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_instance_status_release ON public.instance_release_status USING btree (release_id);

