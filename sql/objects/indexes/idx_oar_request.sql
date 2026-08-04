--
-- Name: idx_oar_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_oar_request ON public.offline_activation_requests USING btree (request_id);

