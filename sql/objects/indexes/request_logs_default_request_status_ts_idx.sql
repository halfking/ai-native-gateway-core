--
-- Name: request_logs_default_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_default_request_status_ts_idx;

