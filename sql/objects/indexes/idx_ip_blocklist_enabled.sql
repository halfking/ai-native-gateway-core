--
-- Name: idx_ip_blocklist_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ip_blocklist_enabled ON public.ip_blocklist USING btree (enabled, scope);

