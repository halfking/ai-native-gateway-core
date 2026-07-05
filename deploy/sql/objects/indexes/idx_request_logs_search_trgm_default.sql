--
-- Name: idx_request_logs_search_trgm_default; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_default ON public.request_logs_default USING gin (search_text public.gin_trgm_ops);

