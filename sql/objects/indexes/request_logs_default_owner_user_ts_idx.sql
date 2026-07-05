--
-- Name: request_logs_default_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_default_owner_user_ts_idx;

