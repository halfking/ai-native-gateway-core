--
-- Name: idx_detections_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_detections_session ON public.prompt_injection_detections USING btree (session_key);

