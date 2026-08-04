--
-- Name: idx_detections_approval; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_detections_approval ON public.prompt_injection_detections USING btree (approval_id) WHERE (approval_id IS NOT NULL);

