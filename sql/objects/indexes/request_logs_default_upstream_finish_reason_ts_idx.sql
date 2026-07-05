--
-- Name: request_logs_default_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_default_upstream_finish_reason_ts_idx;

