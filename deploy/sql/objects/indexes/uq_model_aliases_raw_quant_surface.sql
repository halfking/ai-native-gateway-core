--
-- Name: uq_model_aliases_raw_quant_surface; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_model_aliases_raw_quant_surface ON public.model_aliases USING btree (raw_name, COALESCE(quantization, ''::text), COALESCE(surface, ''::text));

