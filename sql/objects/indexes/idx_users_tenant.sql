--
-- Name: idx_users_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_tenant ON public.users USING btree (tenant_id);

