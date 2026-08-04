--
-- Name: array_unique_append(text[], text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.array_unique_append(arr text[], new_elem text) RETURNS text[]
    LANGUAGE plpgsql IMMUTABLE
    AS $$ BEGIN IF new_elem IS NULL THEN RETURN arr; END IF; IF new_elem = ANY(arr) THEN RETURN arr; ELSE RETURN array_append(arr, new_elem); END IF; END; $$;

