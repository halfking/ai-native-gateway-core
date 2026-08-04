--
-- Name: idx_response_format_anomalies_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_response_format_anomalies_unresolved ON public.response_format_anomalies USING btree (detected_at DESC) WHERE (NOT resolved);

