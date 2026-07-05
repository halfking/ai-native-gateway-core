--
-- Name: token_audit_events_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX token_audit_events_ts_idx ON public.token_audit_events USING btree (ts DESC);

