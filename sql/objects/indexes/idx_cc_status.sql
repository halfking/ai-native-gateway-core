--
-- Name: idx_cc_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cc_status ON public.center_commands USING btree (status, issued_at DESC);

