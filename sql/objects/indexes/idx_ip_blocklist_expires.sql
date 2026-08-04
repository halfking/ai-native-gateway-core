--
-- Name: idx_ip_blocklist_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ip_blocklist_expires ON public.ip_blocklist USING btree (expires_at) WHERE (expires_at IS NOT NULL);

