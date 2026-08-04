--
-- Name: idx_approval_queue_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_queue_expires ON public.approval_queue USING btree (expires_at) WHERE (status = 'pending'::text);

