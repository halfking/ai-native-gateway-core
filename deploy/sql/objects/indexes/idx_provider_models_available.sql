--
-- Name: idx_provider_models_available; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_available ON public.provider_models USING btree (available) WHERE (available = true);

