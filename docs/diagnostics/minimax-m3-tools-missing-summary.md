# minimax-m3 Tools Missing - Investigation Summary

**Request ID**: `0becce7ba3e9e5dcce83e9153d48b522`  
**Model**: `minimax-m3`  
**Provider**: 火山方舟 TokenPlan (Provider ID 34)  
**Issue**: Response missing tools information, conversation stopped  
**Date**: 2026-07-25

## Executive Summary

Cannot access server 154 (port 25022 connection refused) to check logs directly. Based on codebase analysis, the gateway's field stripping logic is **NOT** removing the `tools` field. The most likely root causes are:

1. **Upstream provider didn't return tools** (most likely)
2. **Network interruption during streaming**
3. **Missing endpoint ID configuration**

## Key Findings from Code Analysis

### 1. Provider Configuration ✅
- **Provider ID**: 34
- **Provider Name**: 火山方舟 TokenPlan  
- **Catalog Code**: `volcano-tokenplan`
- **Base URL**: `https://ark.cn-beijing.volces.com/api/coding/v3`
- **Type**: `official` (not aggregator)

### 2. Field Stripping Analysis ✅ NOT THE ISSUE

**File**: `domains/streaming/strip_doubao_fields.go`

The `StripDoubaoFieldsBody` function only removes these specific fields:
```go
var doubaoPrivateFields = []string{
    "doubao_request_id",
    "seeddance_request_id",
    "content_safety_score",
    "model_endpoint",
    "ab_test_group",
    "seed_token_usage",
    "request_id",
    "volc_request_id",
    "internal_model_version",
    "sensitive_check",
}
```

**Conclusion**: The `tools` field is **NOT** in this list, so stripping is not removing tools.

### 3. Strip Logic Application

**File**: `domains/streaming/executors/executor.go:959-976`

The strip function is applied when:
1. Catalog code is explicitly "doubao", OR
2. Auto-detection finds `"doubao_request_id"` or `"seeddance_request_id"` in response

Since `volcano-tokenplan` catalog is different from `doubao`, it only triggers on auto-detection. However, even if triggered, it doesn't remove `tools`.

### 4. Volcano Ark Endpoint ID Requirement ⚠️

**File**: `internal/probeutil/endpoint_id.go`

Critical finding:
```
Volcano Ark (火山方舟) providers expose models via endpoint IDs.
Error code: "InvalidEndpointOrModel" when endpoint ID is missing.
```

**This could be the root cause**: If `credential_model_bindings.outbound_model_name` is not set for minimax-m3, the upstream API might return incomplete or error responses.

## Most Likely Root Causes (Ranked)

### 1. Missing or Incorrect Endpoint ID (HIGH probability)
**Hypothesis**: The credential used doesn't have `outbound_model_name` (endpoint ID) configured for minimax-m3.

**Why**: Volcano Ark requires explicit endpoint IDs. Without it, API returns errors or incomplete responses.

**Check**:
```sql
SELECT 
    c.credential_id,
    c.credential_name,
    cmb.raw_model_name,
    cmb.outbound_model_name,
    cmb.available
FROM request_logs rl
JOIN credentials c ON rl.credential_id = c.credential_id
LEFT JOIN credential_model_bindings cmb 
    ON c.credential_id = cmb.credential_id 
    AND cmb.raw_model_name = 'minimax-m3'
WHERE rl.request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

**Expected**: `outbound_model_name` should be something like `ep-20250115-xxxxx`  
**If NULL**: This is the root cause

### 2. Upstream Provider Response Issue (MEDIUM probability)
**Hypothesis**: Volcano Ark TokenPlan API returned a response without tools, despite tools being in the request.

**Why**: Provider-side issue, API compatibility problem, or model limitation.

**Check**:
```sql
SELECT 
    request_body::jsonb->'tools' as request_tools,
    response_body::jsonb->'choices'->0->'message'->'tool_calls' as response_tools
FROM stream_capture_audit
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

**If tools in request but not in response**: Provider issue

### 3. Network/Streaming Interruption (LOW probability)
**Hypothesis**: Connection interrupted before tools data was fully streamed.

**Why**: Timeout, network issue, or premature connection close.

**Check**:
```sql
SELECT success, latency_ms, error_code 
FROM request_logs 
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

**If latency < 500ms and success=true**: Suspicious - likely incomplete response  
**If success=false**: Check error_code for timeout/connection errors

## Immediate Actions Required

### 1. Restore Server Connectivity
```bash
# Check if server is up
ping 47.97.111.154

# Try alternative access methods
ssh -p 22 root@llm.kxpms.cn  # Direct to domain
ssh 154  # Via SSH config

# Check if port 25022 is being blocked
telnet 47.97.111.154 25022
```

### 2. Once Connected, Run This Query
```sql
-- Critical diagnostic query
SELECT 
    rl.request_id,
    rl.client_model,
    rl.provider_id,
    rl.credential_id,
    rl.success,
    rl.error_code,
    rl.error_message,
    rl.latency_ms,
    c.credential_name,
    p.provider_name,
    p.catalog_code,
    cmb.outbound_model_name as endpoint_id,
    cmb.available,
    jsonb_array_length(rl.request_body::jsonb->'tools') as tools_requested,
    sca.chunk_count,
    sca.finish_reason,
    sca.has_tool_calls
FROM request_logs rl
LEFT JOIN credentials c ON rl.credential_id = c.credential_id
LEFT JOIN providers p ON c.provider_id = p.provider_id
LEFT JOIN credential_model_bindings cmb 
    ON c.credential_id = cmb.credential_id 
    AND cmb.raw_model_name = rl.client_model
LEFT JOIN stream_capture_audit sca ON rl.request_id = sca.request_id
WHERE rl.request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

### 3. Check Gateway Logs
```bash
ssh 154
sudo journalctl -u llm-gateway --since '2 hours ago' --no-pager \
  | grep '0becce7ba3e9e5dcce83e9153d48b522' \
  | grep -E 'error|warn|endpoint|tool|strip' -i
```

## Expected Outcomes & Solutions

### Scenario A: Missing Endpoint ID
**Finding**: `outbound_model_name` is NULL or incorrect

**Solution**:
1. Get correct endpoint ID from Volcano Ark console
2. Update `credential_model_bindings`:
   ```sql
   UPDATE credential_model_bindings
   SET outbound_model_name = 'ep-20250115-xxxxx'
   WHERE credential_id = <cred_id>
   AND raw_model_name = 'minimax-m3';
   ```
3. Restart gateway or wait for next probe cycle

### Scenario B: Provider API Issue
**Finding**: Endpoint ID is set, but response lacks tools

**Solution**:
1. Test direct API call to Volcano Ark with same request
2. Contact Volcano Ark support if their API has issues
3. Consider switching to provider 29 (volcengine-coding aggregator) as fallback

### Scenario C: Network Interruption
**Finding**: Request failed or timed out

**Solution**:
1. Check timeout settings
2. Review nginx/proxy configuration
3. Check network stability between gateway and Volcano Ark

## Related Documentation

- **Comprehensive Diagnostic Guide**: `docs/diagnostics/minimax-m3-tools-missing-diagnostic.md`
- **Endpoint ID Documentation**: `internal/probeutil/endpoint_id.go`
- **Strip Logic**: `domains/streaming/strip_doubao_fields.go`
- **Provider Catalog**: `deploy/sql/schemas/baseline/02-seed.sql`

## Next Steps

1. ✅ Restore server connectivity
2. ⏳ Run diagnostic SQL query above
3. ⏳ Check gateway logs for request_id
4. ⏳ Verify endpoint ID configuration
5. ⏳ Test with working minimax-m3 request for comparison
6. ⏳ Implement fix based on findings

---

**Status**: Awaiting server access  
**Blocking Issue**: Port 25022 connection refused on 47.97.111.154  
**Created**: 2026-07-25 by ZCode systematic debugging
