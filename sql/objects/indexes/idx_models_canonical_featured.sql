CREATE INDEX idx_models_canonical_featured ON public.models_canonical USING btree (featured) WHERE (featured = true);
