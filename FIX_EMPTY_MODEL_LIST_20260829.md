# Fix: Empty Model List from Provider Refresh (2026-08-29)

## Issue

Provider 12763 (https://llm.kxpms.cn/providers/12763) returned an empty model list when triggering "从供应商读取" (refresh models from provider). The credentials were manually disabled via `PATCH /api/admin/providers/{id}/enable` but the refresh endpoint still attempted to read them, returning zero results.

## Root Cause

Merge commit `d2cbaf88b` (2026-08-22) regressed the credential eligibility filter in `admin/provider_refresh.go::fetchActiveCredentialsForProvider()`. The merge chose upstream (origin/main) over local changes, discarding critical filters from commit `0019aabfa`:

1. **Missing `manual_disabled` exclusion**: The query did not filter `COALESCE(c.manual_disabled, FALSE) = FALSE`, so manually-disabled credentials were included in the refresh scan
2. **Missing `api_models_ok` relaxation**: The query reverted to strict `status='active'` filtering, dropping auto-disabled credentials that still had working models

### Regressed Query (before fix)
```sql
WHERE c.provider_id = $1
  AND c.status = 'active'
  AND COALESCE(c.lifecycle_status, 'active') NOT IN ('suspended', 'retired', 'disabled')
  AND COALESCE(c.availability_state, 'ready') = 'ready'
  AND (c.quota_state IS NULL OR c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted'))
  AND p.enabled = TRUE
```

This filter had two problems:
- **No `manual_disabled` check**: included credentials that operators explicitly disabled
- **Too strict**: excluded auto-disabled credentials that still had `api_models_ok=TRUE`

## Fix Applied

Restored the filters from commit `0019aabfa`:

```sql
WHERE c.provider_id = $1
  AND c.tenant_id = 'default'
  AND p.tenant_id = 'default'
  AND c.secret_ciphertext IS NOT NULL
  AND COALESCE(c.manual_disabled, FALSE) = FALSE  -- ✅ exclude manually-disabled
  AND p.enabled = TRUE
  AND (
      c.status = 'active'
      OR COALESCE(c.api_models_ok, FALSE) = TRUE  -- ✅ include auto-disabled with working models
  )
  AND (
      c.lifecycle_status IS NULL
      OR c.lifecycle_status = 'active'
      OR (c.lifecycle_status = 'disabled' AND COALESCE(c.api_models_ok, FALSE) = TRUE)
  )
```

### Key Changes

1. **Added `manual_disabled = FALSE` filter**: Credentials manually disabled by operators (via `PATCH /api/admin/providers/{id}/enable`) are now excluded from refresh scans
2. **Restored `api_models_ok` relaxation**: Auto-disabled credentials (status='disabled' due to quota probes or health checks) are included if `api_models_ok=TRUE` indicates they previously had working models
3. **Added safety checks**: 
   - `tenant_id = 'default'` (both credentials and providers)
   - `secret_ciphertext IS NOT NULL` (ensure API key exists)

## Impact

- **For manually-disabled providers**: Refresh will skip manually-disabled credentials, returning only models from active credentials. If all credentials are manually-disabled, the result will be empty (expected behavior).
- **For auto-disabled providers**: Refresh can now recover model lists from credentials that were auto-disabled by quota probes but still have callable models, without waiting for health checks to re-enable them.

## Testing

Compiled successfully:
```bash
$ go build -o /dev/null ./admin
# No errors
```

## Related Commits

- `0019aabfa` (2026-08-22): Original fix that added manual_disabled exclusion and api_models_ok relaxation
- `d2cbaf88b` (2026-08-22): Merge that regressed the filter by choosing upstream
- This fix (2026-08-29): Restoration with documentation

## Recommendation

After deployment, verify provider 12763:

1. Check credential states:
   ```sql
   SELECT id, label, status, lifecycle_status, manual_disabled, api_models_ok
   FROM credentials
   WHERE provider_id = 12763;
   ```

2. If all credentials are `manual_disabled=TRUE`, the empty result is correct
3. If some credentials are `manual_disabled=FALSE` and `api_models_ok=TRUE`, refresh should now return their models
4. Monitor the refresh logs for `credentials_scanned` count to confirm non-zero when eligible credentials exist
