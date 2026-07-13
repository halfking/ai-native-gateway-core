package maas

// RequestLogCreditsSQL returns a per-row credits expression for request_logs
// queries. It prefers persisted credits_charged and falls back to a
// maas_settings-based token estimate for non-default tenants when charging
// failed to persist (e.g. SQLSTATE 42725 before the ::bigint wallet fix).
func RequestLogCreditsSQL(alias string) string {
	return `COALESCE(` + alias + `.credits_charged, ` + estimatedRequestLogCreditsSQL(alias) + `)`
}

func estimatedRequestLogCreditsSQL(alias string) string {
	base := `COALESCE(NULLIF(ms.base_credits_per_1m, 0), 10000)`
	disc := `COALESCE(NULLIF(ms.global_discount, 0), 1.0)`
	rateIn := `GREATEST(CEIL((` + base + `)::float8 * ` + disc + `), 1)`
	rateOut := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_out, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	rateCacheIn := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_cache_in, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	rateCacheOut := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_cache_out, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	return `CASE
		WHEN ` + alias + `.tenant_id IS NULL OR ` + alias + `.tenant_id IN ('', 'default') THEN NULL
		WHEN COALESCE(` + alias + `.prompt_tokens, 0)
		   + COALESCE(` + alias + `.completion_tokens, 0)
		   + COALESCE(` + alias + `.cache_read_tokens, 0)
		   + COALESCE(` + alias + `.cache_write_tokens, 0) <= 0 THEN NULL
		ELSE (
			SELECT CEIL((
				COALESCE(` + alias + `.prompt_tokens, 0)::float8 * (` + rateIn + `) +
				COALESCE(` + alias + `.completion_tokens, 0)::float8 * (` + rateOut + `) +
				COALESCE(` + alias + `.cache_read_tokens, 0)::float8 * (` + rateCacheIn + `) +
				COALESCE(` + alias + `.cache_write_tokens, 0)::float8 * (` + rateCacheOut + `)
			) / 1000000.0)::bigint
			FROM maas_settings ms
			WHERE ms.id = 1
		)
	END`
}
