--
-- Name: request_logs_default_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx;

