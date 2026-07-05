--
-- Name: request_logs_default_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_default_api_key_id_ts_idx;

