--
-- Name: request_logs_default_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx1;

