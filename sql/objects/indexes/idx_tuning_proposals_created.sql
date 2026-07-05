--
-- Name: idx_tuning_proposals_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_created ON public.tuning_proposals USING btree (created_at) WHERE (status = 'pending'::text);

