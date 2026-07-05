--
-- Name: request_logs_default_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_default_parent_request_id_ts_idx;

