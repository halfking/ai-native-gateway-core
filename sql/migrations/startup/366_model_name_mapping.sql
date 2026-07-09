-- Migration: 366_model_name_mapping
-- Description: Add model_name_mapping table for raw_model_name to standardized_name mapping
-- Date: 2026-07-10
-- 
-- This table provides a centralized mapping from raw model names to their standardized names.
-- When provider_models.standardized_name is empty, the system will look up this table.
-- If no entry exists here, the raw_model_name itself is used as the standardized name.

CREATE TABLE IF NOT EXISTS public.model_name_mapping (
    id bigserial PRIMARY KEY,
    raw_model_name text NOT NULL,
    standardized_name text NOT NULL,
    description text,
    auto_generated boolean DEFAULT FALSE,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by text,
    CONSTRAINT model_name_mapping_raw_unique UNIQUE (raw_model_name)
);

COMMENT ON TABLE public.model_name_mapping IS 'Maps raw model names (from provider APIs) to standardized names. Used when provider_models.standardized_name is empty.';

CREATE INDEX IF NOT EXISTS idx_model_name_mapping_standardized ON public.model_name_mapping (standardized_name);

-- Function to auto-populate mapping from provider_models where standardized_name is set
CREATE OR REPLACE FUNCTION public.populate_model_name_mapping_from_provider_models()
RETURNS void AS $$
BEGIN
    INSERT INTO public.model_name_mapping (raw_model_name, standardized_name, auto_generated, description)
    SELECT DISTINCT pm.raw_model_name, pm.standardized_name, TRUE, 'Auto-generated from provider_models.standardized_name'
    FROM public.provider_models pm
    WHERE pm.standardized_name IS NOT NULL 
      AND pm.standardized_name != ''
      AND pm.standardized_name != pm.raw_model_name
    ON CONFLICT (raw_model_name) DO UPDATE 
        SET standardized_name = EXCLUDED.standardized_name,
            updated_at = now(),
            auto_generated = TRUE;
END;
$$ LANGUAGE plpgsql;

-- Trigger to update updated_at
CREATE OR REPLACE FUNCTION public.model_name_mapping_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS model_name_mapping_updated_at ON public.model_name_mapping;
CREATE TRIGGER model_name_mapping_updated_at
    BEFORE UPDATE ON public.model_name_mapping
    FOR EACH ROW
    EXECUTE FUNCTION public.model_name_mapping_updated_at();

-- Function to get standardized name from mapping, provider_models, or raw_model_name
CREATE OR REPLACE FUNCTION public.get_standardized_name(p_raw_model_name text)
RETURNS text AS $$
DECLARE
    v_standardized text;
BEGIN
    -- First try model_name_mapping table
    SELECT mnm.standardized_name INTO v_standardized
    FROM public.model_name_mapping mnm
    WHERE lower(mnm.raw_model_name) = lower(p_raw_model_name);
    
    IF v_standardized IS NOT NULL AND v_standardized != '' THEN
        RETURN v_standardized;
    END IF;
    
    -- Then try provider_models.standardized_name
    SELECT pm.standardized_name INTO v_standardized
    FROM public.provider_models pm
    WHERE lower(pm.raw_model_name) = lower(p_raw_model_name)
    LIMIT 1;
    
    IF v_standardized IS NOT NULL AND v_standardized != '' THEN
        RETURN v_standardized;
    END IF;
    
    -- Fallback to raw_model_name
    RETURN p_raw_model_name;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.get_standardized_name(text) IS 
'Returns the standardized name for a raw model name. 
Priority: 1. model_name_mapping table, 2. provider_models.standardized_name, 3. raw_model_name (fallback)';