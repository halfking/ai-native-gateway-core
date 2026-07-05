--
-- Name: idx_models_canonical_tags_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_tags_gin ON public.models_canonical USING gin (tags);

