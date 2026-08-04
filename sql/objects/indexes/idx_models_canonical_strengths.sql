--
-- Name: idx_models_canonical_strengths; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_strengths ON public.models_canonical USING gin (strengths);

