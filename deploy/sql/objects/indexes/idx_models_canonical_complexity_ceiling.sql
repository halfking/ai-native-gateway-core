--
-- Name: idx_models_canonical_complexity_ceiling; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_complexity_ceiling ON public.models_canonical USING btree (complexity_ceiling) WHERE (complexity_ceiling IS NOT NULL);

