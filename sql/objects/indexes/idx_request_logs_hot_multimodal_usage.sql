--
-- Name: idx_request_logs_hot_multimodal_usage; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_multimodal_usage ON public.request_logs_hot USING btree (tenant_id, ts DESC) WHERE ((image_tokens > 0) OR (audio_tokens > 0) OR (video_tokens > 0));

