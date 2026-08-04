--
-- Name: idx_ip_blocklist_active_entry; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_ip_blocklist_active_entry ON public.ip_blocklist USING btree (ip_or_cidr, scope) WHERE (enabled = true);

