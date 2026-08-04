--
-- Name: idx_wtmr_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wtmr_tier ON public.work_type_model_route USING btree (work_type_key, tier, weight DESC);

