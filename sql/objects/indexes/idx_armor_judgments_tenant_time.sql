--
-- Name: idx_armor_judgments_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_armor_judgments_tenant_time ON public.armor_judgments USING btree (tenant_id, created_at DESC);

