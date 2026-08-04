--
-- Name: idx_gray_rules_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gray_rules_status ON public.gray_release_rules USING btree (status, created_at DESC);

