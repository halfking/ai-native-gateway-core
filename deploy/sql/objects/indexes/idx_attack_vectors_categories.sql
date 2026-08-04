--
-- Name: idx_attack_vectors_categories; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_attack_vectors_categories ON public.injection_attack_vectors USING gin (categories);

