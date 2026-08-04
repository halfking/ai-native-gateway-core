--
-- Name: idx_detections_categories; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_detections_categories ON public.prompt_injection_detections USING gin (categories);

