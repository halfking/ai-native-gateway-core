--
-- Name: idx_models_canonical_tags_locked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_tags_locked ON public.models_canonical USING btree (tags_locked) WHERE (tags_locked = true);

