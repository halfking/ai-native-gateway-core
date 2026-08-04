--
-- Name: _064_convert_partition_to_heap(text); Type: PROCEDURE; Schema: public; Owner: -
--

CREATE PROCEDURE public._064_convert_partition_to_heap(IN p_part text)
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_oid        oid;
    v_am         text;
    v_parent     text;
    v_new        text := p_part || '__heap_tmp';
    v_constraint text;
    v_total      bigint;
    v_dedup      bigint;
    v_distinct   bigint;
BEGIN
    SELECT c.oid, am.amname
      INTO v_oid, v_am
    FROM pg_class c
    LEFT JOIN pg_am am ON am.oid = c.relam
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = p_part AND c.relkind = 'r';

    IF v_oid IS NULL THEN
        RAISE NOTICE '[064] %: not found, skip', p_part;
        RETURN;
    END IF;
    IF v_am IS DISTINCT FROM 'columnar' THEN
        RAISE NOTICE '[064] %: not columnar (%), skip', p_part, COALESCE(v_am, 'heap');
        RETURN;
    END IF;

    SELECT inhparent::regclass::text INTO v_parent
      FROM pg_inherits WHERE inhrelid = v_oid;

    SELECT COALESCE(reltuples::bigint, 0) INTO v_total
      FROM pg_class WHERE oid = v_oid;
    EXECUTE format('SELECT count(*) FROM public.%I', p_part) INTO v_total;
    RAISE NOTICE '[064] %: rows=% (columnar)', p_part, v_total;

    EXECUTE format(
        'CREATE TABLE public.%I (LIKE public.%I INCLUDING DEFAULTS INCLUDING CONSTRAINTS)',
        v_new, p_part
    );

    EXECUTE format(
        'INSERT INTO public.%I SELECT DISTINCT * FROM public.%I',
        v_new, p_part
    );
    GET DIAGNOSTICS v_distinct = ROW_COUNT;
    v_dedup := v_total - v_distinct;
    RAISE NOTICE '[064] %: DISTINCT insert=% rows (deduped=% from %)',
        p_part, v_distinct, v_dedup, v_total;
    IF v_total > 0 AND v_dedup > 0 THEN
        RAISE WARNING '[064] %: % duplicate (bucket,cred,model) tuples collapsed. '
            'This usually means a previous failed insert left a partial row. '
            'Inspect with: SELECT bucket, credential_id, raw_model, COUNT(*) '
            'FROM public.%I GROUP BY 1,2,3 HAVING COUNT(*) > 1;',
            p_part, v_dedup, p_part;
    END IF;

    RAISE NOTICE '[064] %: staging size=%', p_part,
        pg_size_pretty(pg_total_relation_size('public.' || v_new::regclass));

    SELECT pg_get_expr(c.relpartbound, c.oid, true) INTO v_constraint
      FROM pg_class c WHERE c.oid = v_oid;

    EXECUTE format('ALTER TABLE public.%I DETACH PARTITION public.%I', v_parent, p_part);
    EXECUTE format('DROP TABLE public.%I', p_part);
    EXECUTE format('ALTER TABLE public.%I RENAME TO %I', v_new, p_part);
    EXECUTE format(
        'ALTER TABLE public.%I ATTACH PARTITION public.%I %s',
        v_parent, p_part, v_constraint
    );

    RAISE NOTICE '[064] %: conversion complete (now heap). Final size=%',
        p_part, pg_size_pretty(pg_total_relation_size('public.' || p_part::regclass));

END;
$$;

