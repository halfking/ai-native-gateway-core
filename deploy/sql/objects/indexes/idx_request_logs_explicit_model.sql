--
-- Name: idx_request_logs_explicit_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_explicit_model ON ONLY public.request_logs USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: INDEX idx_request_logs_explicit_model; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON INDEX public.idx_request_logs_explicit_model IS 'Supports the routing-v2 explicit-model analytics path (handleMatrix/handleFlow/handleAudit) where client_model is used in place of outbound_model.';

