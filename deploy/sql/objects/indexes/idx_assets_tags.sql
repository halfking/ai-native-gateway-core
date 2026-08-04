--
-- Name: idx_assets_tags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_assets_tags ON public.assets USING gin (tags jsonb_path_ops);

