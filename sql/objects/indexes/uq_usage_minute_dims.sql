--
-- Name: uq_usage_minute_dims; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_usage_minute_dims ON public.usage_minute USING btree (bucket, tenant_id, COALESCE(application_id, (0)::bigint), COALESCE(api_key_id, (0)::bigint), COALESCE(end_user_id, ''::text), COALESCE(department, ''::text), COALESCE(employee, ''::text), COALESCE("position", ''::text), COALESCE(credential_id, (0)::bigint), COALESCE(provider_id, (0)::bigint), COALESCE(canonical_id, (0)::bigint));

