--
-- Name: idx_local_models_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_local_models_canonical ON public.local_models USING btree (canonical_id) WHERE (canonical_id IS NOT NULL);

