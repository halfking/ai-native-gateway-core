--
-- Name: idx_approval_configs_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_configs_enabled ON public.approval_configs USING btree (enabled) WHERE (enabled = true);

