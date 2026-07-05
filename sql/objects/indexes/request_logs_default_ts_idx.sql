--
-- Name: request_logs_default_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_default_ts_idx;

