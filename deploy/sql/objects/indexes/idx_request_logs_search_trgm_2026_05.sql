--
-- Name: idx_request_logs_search_trgm_2026_05; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_05 ON public.request_logs_2026_05 USING gin (search_text public.gin_trgm_ops);

