--
-- Name: idx_license_holders_email_lower; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_license_holders_email_lower ON public.license_holders USING btree (lower(email));

