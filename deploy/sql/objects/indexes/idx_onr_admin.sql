--
-- Name: idx_onr_admin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_onr_admin ON public.ops_node_registrations USING btree (admin_user);

