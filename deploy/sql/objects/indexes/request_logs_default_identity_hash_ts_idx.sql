--
-- Name: request_logs_default_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_default_identity_hash_ts_idx;

