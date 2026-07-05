--
-- Name: idx_request_logs_tool_calls; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_tool_calls ON ONLY public.request_logs USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));

