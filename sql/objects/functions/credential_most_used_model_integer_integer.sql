--
-- Name: credential_most_used_model(integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.credential_most_used_model(p_credential_id integer, p_lookback_hours integer DEFAULT 24) RETURNS text
    LANGUAGE plpgsql STABLE
    AS $$
DECLARE
    v_model TEXT;
BEGIN
    -- 主路径：request_logs_hot (热表，毫秒级)
    SELECT rl.model
      INTO v_model
      FROM request_logs_hot rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    IF v_model IS NOT NULL THEN
        RETURN v_model;
    END IF;

    -- Fallback: 冷表 request_logs (90 天后迁过去的)
    SELECT rl.model
      INTO v_model
      FROM request_logs rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    RETURN v_model;
END;
$$;

