--
-- Name: idx_session_memora_extraction_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_memora_extraction_at ON public.session_memora_extraction_log USING btree (extracted_at DESC);

