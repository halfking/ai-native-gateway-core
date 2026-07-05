--
-- Name: request_logs_default_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_default_model_chosen_ts_idx;

