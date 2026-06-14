SELECT rl.client_model, mc.canonical_name, rl.outbound_model, mo_pick.provider_model
FROM request_logs rl
LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
LEFT JOIN LATERAL (
  SELECT COALESCE(
    NULLIF(TRIM(mo.outbound_model_name), ''),
    NULLIF(TRIM(mo.raw_model_name), '')
  ) AS provider_model
  FROM model_offers mo
  WHERE mo.credential_id = rl.credential_id
    AND (
      (rl.canonical_id IS NOT NULL AND mo.canonical_id = rl.canonical_id)
      OR (
        rl.canonical_id IS NULL AND (
          lower(mo.standardized_name) = lower(COALESCE(mc.canonical_name, rl.client_model, ''))
          OR lower(mo.raw_model_name) = lower(COALESCE(rl.outbound_model, rl.client_model, ''))
        )
      )
    )
  ORDER BY
    CASE
      WHEN rl.outbound_model IS NOT NULL
       AND lower(COALESCE(NULLIF(TRIM(mo.outbound_model_name), ''), TRIM(mo.raw_model_name)))
        = lower(rl.outbound_model)
      THEN 0 ELSE 1
    END,
    CASE WHEN NULLIF(TRIM(mo.outbound_model_name), '') IS NOT NULL THEN 0 ELSE 1 END,
    CASE
      WHEN lower(TRIM(mo.raw_model_name)) <> lower(TRIM(COALESCE(mo.standardized_name, mc.canonical_name, rl.client_model, '')))
      THEN 0 ELSE 1
    END,
    mo.available DESC NULLS LAST,
    mo.id DESC
  LIMIT 1
) mo_pick ON TRUE
WHERE rl.success = true
  AND rl.client_model = 'minimax-m3'
  AND rl.ts > now() - interval '1 hour'
ORDER BY rl.ts DESC
LIMIT 5;
