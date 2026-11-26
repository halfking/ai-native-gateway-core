# Rate Limit Error Message Clarification

## Issue Report
Date: 2026-11-26
Reporter: User via /goal

## Problem Statement
Users reported that API keys without rate limits (RPM=0, TPM=NULL, Concurrent=0) are receiving "rate limited" errors. Investigation shows this is a **misunderstanding of error messages**, not an actual rate limiting bug.

## Investigation Results

### 1. Key Configuration (ID=105)
```sql
SELECT id, rate_limit_rpm, rate_limit_tpm, rate_limit_concurrent, status 
FROM api_keys WHERE id = 105;

 id  | rate_limit_rpm | rate_limit_tpm | rate_limit_concurrent | status
-----+----------------+----------------+-----------------------+--------
 105 |              0 |           NULL |                     0 | active
```

**Interpretation:**
- `rate_limit_rpm = 0` → Explicit unlimited (correctly handled)
- `rate_limit_tpm = NULL` → Unlimited (correctly returns 0 from `EffectiveTPM()`)
- `rate_limit_concurrent = 0` → Explicit unlimited (correctly handled)

### 2. Code Review - Rate Limit Logic

All rate limit checking code correctly treats 0 as unlimited:

**domains/streaming/rate_limit.go:**
```go
// Line 116-119
if limit <= 0 {
    // Explicit unlimited (DB=0). No headers, no check.
    return rateLimitOutcome{Limit: 0, Remaining: -1, ResetSec: 0}
}
```

**domains/streaming/key_concurrent_gate.go:**
```go
// Line 170-173  
if limit <= 0 {
    next.ServeHTTP(w, r)
    return
}
```

**domains/credential/limiter.go:**
```go
// Line 583
if keyID > 0 && keyConcurrentLimit > 0 {
    // Only checks if limit > 0
}
```

**domains/authentication/verifier.go:**
```go
// EffectiveRPM - Lines 134-136
if *ki.RateLimitRPM == 0 {
    return 0 // explicit unlimited
}

// EffectiveTPM - Lines 182-184  
if ki.RateLimitTPM != nil && *ki.RateLimitTPM > 0 {
    return *ki.RateLimitTPM
}
return 0 // NULL or <=0 means unlimited
```

**Conclusion:** All rate limit code paths correctly treat 0 as unlimited.

### 3. Actual Error from Production Test

Request to `gpt-4o-mini` with key 105 returned:

```json
{
  "error": {
    "cause": "upstream_quota_exhausted+provider_disabled",
    "code": "no_candidate",
    "kind": "no_candidate",
    "message": "No available provider for model 'gpt-4o-mini': every candidate for it is blocked (upstream account quota or balance exhausted; provider or credential disabled by the gateway operator)...",
    "retryable": false,
    "type": "server_error"
  }
}
```

**HTTP Status:** 503 (Service Unavailable), **not** 429 (Too Many Requests)

**Root Cause:** The upstream provider credentials for `gpt-4o-min` have exhausted their quota or are disabled. This is **NOT** an API key rate limit issue.

## Problem Analysis

The confusion arises from:

1. **Error Code Naming:** "upstream_quota_exhausted" contains the word "quota" which users associate with rate limiting
2. **HTTP 503 vs 429:** The correct status (503) is returned, but users may not understand the distinction
3. **Error Message Clarity:** The message says "upstream account quota" but users may still think it refers to their key's limits
4. **Lack of Context:** Error doesn't explicitly state "Your API key limits are NOT the issue"

## Recommendations

### Short-term Fix (Error Message Enhancement)
Add explicit clarification in `no_candidate` errors to distinguish between:
- API key rate limits (gateway-side, user-configurable)
- Provider credential quota (platform-side, affects all users)

### Medium-term Fix (Logging Enhancement)
Add structured logging that clearly separates:
- Gateway-side rate limit rejections (HTTP 429, key-level limits)
- Provider-side capacity issues (HTTP 503, upstream quota)

### Long-term Fix (Observability)
Add metrics/dashboards that show:
- Per-key rate limit hit rates
- Per-provider quota exhaustion rates
- Clear attribution of "which layer caused the rejection"

## Status
- **API Key Rate Limiting:** ✅ Working correctly, 0 is treated as unlimited
- **Error Messages:** ⚠️  Could be clearer about source of limitation
- **User Understanding:** ❌ Users misinterpreting upstream quota errors as key limits

## Next Steps
1. Enhance error messages to clarify "this is not your key's rate limit"
2. Add logging to distinguish between gateway-side and provider-side rejections
3. Consider adding a troubleshooting guide for common error codes
