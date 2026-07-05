--
-- Name: idx_request_logs_owner_user_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_owner_user_ts ON ONLY public.request_logs USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));

