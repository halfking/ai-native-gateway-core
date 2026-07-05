--
-- Name: request_logs_default_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_default_client_model_ts_idx;

