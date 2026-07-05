--
-- Name: idx_request_logs_search_trgm_2026_04; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_04 ON public.request_logs_2026_04 USING gin (search_text public.gin_trgm_ops);

