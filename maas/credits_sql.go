package maas

// RequestLogCreditsSQL returns a per-row credits expression for request_logs
// queries. It prefers persisted credits_charged and falls back to a
// maas_settings-based token estimate when charging did not persist.
//
// When includeDefaultTenant is true (platform-wide dashboard scope), traffic
// under tenant_id=default is included in the estimate; otherwise only billed
// tenants contribute.
func RequestLogCreditsSQL(alias string, includeDefaultTenant bool) string {
	return `COALESCE(` + alias + `.credits_charged, ` + estimatedRequestLogCreditsSQL(alias, includeDefaultTenant) + `)`
}

func estimatedRequestLogCreditsSQL(alias string, includeDefaultTenant bool) string {
	// Wave 3 B1: fold the persisted per-request rate multiplier into the
	// summed cost BEFORE the single per-1M round-up — the same placement as
	// CalcCreditsMultimodalWithMultiplier (numer * rateMultiplier / 1e6,
	// then Ceil once). R56 audit: the multiplier used to sit inside the
	// per-rate CEIL, which over-charged the estimate whenever rate*disc*mult
	// was non-integer (e.g. 10.4 × 1.5 → CEIL=16 vs Go's effective 15.6);
	// the estimate then drifted up to ~1 credit / 2.5M tokens above the Go
	// charge.
	base := `COALESCE(NULLIF(ms.base_credits_per_1m, 0), 10000)`
	disc := `COALESCE(NULLIF(ms.global_discount, 0), 1.0)`
	mult := `COALESCE(` + alias + `.credits_rate_multiplier, 1.0)::float8`
	rateIn := `GREATEST(CEIL((` + base + `)::float8 * ` + disc + `), 1)`
	rateOut := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_out, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	rateCacheIn := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_cache_in, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	rateCacheOut := `GREATEST(CEIL((COALESCE(NULLIF(ms.base_credits_per_1m_cache_out, 0), ` + base + `))::float8 * ` + disc + `), 1)`
	tenantSkip := `WHEN ` + alias + `.tenant_id IS NULL OR ` + alias + `.tenant_id = '' THEN NULL`
	if !includeDefaultTenant {
		tenantSkip = `WHEN ` + alias + `.tenant_id IS NULL OR ` + alias + `.tenant_id IN ('', 'default') THEN NULL`
	}
	return `CASE
		` + tenantSkip + `
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
			) * ` + mult + ` / 1000000.0)::bigint
			FROM maas_settings ms
			WHERE ms.id = 1
		)
	END`
}
