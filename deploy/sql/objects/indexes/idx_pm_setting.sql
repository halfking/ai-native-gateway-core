--
-- Name: idx_pm_setting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pm_setting ON public.product_modules USING btree (setting_key);

