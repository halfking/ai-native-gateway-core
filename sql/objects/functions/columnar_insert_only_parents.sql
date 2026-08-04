--
-- Name: columnar_insert_only_parents(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.columnar_insert_only_parents() RETURNS text[]
    LANGUAGE sql STABLE
    AS $$
    SELECT ARRAY['routing_decision_log'];
$$;

