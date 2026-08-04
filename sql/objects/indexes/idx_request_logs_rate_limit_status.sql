--
-- Name: idx_request_logs_rate_limit_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_rate_limit_status ON ONLY public.request_logs USING btree (rate_limit_status) WHERE ((rate_limit_status)::text = ANY ((ARRAY['exceeded'::character varying, 'approaching_limit'::character varying])::text[]));

