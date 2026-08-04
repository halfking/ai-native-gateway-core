--
-- Name: idx_models_canonical_released; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_released ON public.models_canonical USING btree (released_at DESC NULLS LAST);

