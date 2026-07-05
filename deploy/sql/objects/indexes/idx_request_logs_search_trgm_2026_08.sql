--
-- Name: idx_request_logs_search_trgm_2026_08; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_08 ON public.request_logs_2026_08 USING gin (search_text public.gin_trgm_ops);

