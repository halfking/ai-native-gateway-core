# Diagnostic Guide: minimax-m3 Tools Missing Investigation

## Issue Summary
- **Request ID**: `0becce7ba3e9e5dcce83e9153d48b522`
- **Model**: `minimax-m3`
- **Provider**: 火山方舟 TokenPlan (Provider ID: 34, catalog: `volcano-tokenplan`)
- **Problem**: Response missing `tools` information, causing conversation to stop
- **Expected**: Should return tools and have subsequent continuous actions

## Background

### Provider Information
- **Provider ID**: 34
- **Provider Name**: 火山方舟 TokenPlan
- **Catalog Code**: `volcano-tokenplan`
- **Base URL**: `https://ark.cn-beijing.volces.com/api/coding/v3`
- **Protocol**: `openai-completions`
- **Type**: `official` (not aggregator)

### Related Provider (for comparison)
- **Provider ID**: 29 - 火山方舟 Coding (aggregator, includes minimax-m3)
- **Catalog Code**: `volcengine-coding`

## Phase 1: Root Cause Investigation

### Step 1: Check Request Logs

Connect to database:
```bash
ssh llm-252
psql -U postgres -d llm_gateway
```

Query request logs:
```sql
-- Basic request information
SELECT 
    request_id,
    client_model,
    provider_id,
    credential_id,
    success,
    error_code,
    error_message,
    latency_ms,
    created_at,
    request_body::jsonb->'tools' as request_tools,
    length(request_body::text) as request_size
FROM request_logs 
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522'
ORDER BY created_at DESC;

-- Check if tools were in the request
SELECT 
    request_id,
    jsonb_pretty(request_body::jsonb->'tools') as tools_requested,
    jsonb_array_length(request_body::jsonb->'tools') as tool_count
FROM request_logs 
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

### Step 2: Check Stream Capture Audit

```sql
-- Check if response was captured
SELECT 
    request_id,
    provider_id,
    chunk_count,
    finish_reason,
    has_tool_calls,
    response_preview::jsonb->'choices'->0->'message'->'tool_calls' as response_tools,
    created_at
FROM stream_capture_audit
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';

-- Check response body if available
SELECT 
    request_id,
    response_body::jsonb->'choices'->0->'message' as message_content,
    response_body::jsonb->'choices'->0->'message'->'tool_calls' as tool_calls
FROM stream_capture_audit
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

### Step 3: Check Gateway Logs

Connect to 154 server:
```bash
ssh 154
```

Search for the request ID in logs:
```bash
# Check journalctl for llm-gateway service
sudo journalctl -u llm-gateway --since '1 hour ago' --no-pager | grep '0becce7ba3e9e5dcce83e9153d48b522'

# Check for tools-related errors
sudo journalctl -u llm-gateway --since '1 hour ago' --no-pager | grep -A 10 -B 10 '0becce7ba3e9e5dcce83e9153d48b522' | grep -i 'tool\|strip\|sanitize'

# Check for provider-specific errors
sudo journalctl -u llm-gateway --since '1 hour ago' --no-pager | grep '0becce7ba3e9e5dcce83e9153d48b522' | grep -i 'volcano\|tokenplan\|minimax'

# Check for streaming errors
sudo journalctl -u llm-gateway --since '1 hour ago' --no-pager | grep '0becce7ba3e9e5dcce83e9153d48b522' | grep -i 'stream\|chunk\|interrupted'
```

### Step 4: Check Routing Attempts

```sql
-- Check if there were multiple routing attempts
SELECT 
    request_id,
    routing_attempts_json::jsonb as attempts,
    routing_summary
FROM request_logs
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';

-- Parse routing attempts
SELECT 
    request_id,
    jsonb_pretty(routing_attempts_json::jsonb) as routing_details
FROM request_logs
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

## Phase 2: Hypothesis Formation

Based on codebase analysis, here are the potential root causes:

### Hypothesis 1: Response Stripping Issue
**Theory**: The `StripDoubaoFieldsBody` function or similar stripping logic is being applied to Volcano TokenPlan responses and accidentally removing `tools` field.

**Evidence to gather**:
- Check if `IsDoubaoCatalog(catalogCode)` returns true for `volcano-tokenplan`
- Verify if strip functions are applied to this provider
- Compare catalog_code: `volcano-tokenplan` vs `doubao` vs `volcengine-coding`

**Test**:
```bash
# Check the strip logic in code
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
grep -rn "IsDoubaoCatalog.*volcano" ./domains/streaming/
grep -rn "volcano-tokenplan" ./domains/streaming/strip_doubao_fields.go
```

### Hypothesis 2: Upstream Provider Response Missing Tools
**Theory**: The Volcano Engine TokenPlan API itself didn't return tools in the response.

**Evidence to gather**:
- Check raw upstream response before gateway processing
- Compare with working minimax-m3 requests from other providers
- Check if endpoint ID is properly configured

**Diagnostic queries**:
```sql
-- Check credential configuration
SELECT 
    c.credential_id,
    c.credential_name,
    c.provider_id,
    p.provider_name,
    cmb.outbound_model_name,
    cmb.available,
    c.status
FROM credentials c
JOIN providers p ON c.provider_id = p.provider_id
LEFT JOIN credential_model_bindings cmb ON c.credential_id = cmb.credential_id
WHERE c.credential_id IN (
    SELECT credential_id 
    FROM request_logs 
    WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522'
);

-- Check if endpoint ID (outbound_model_name) is set
SELECT 
    cmb.credential_id,
    cmb.raw_model_name,
    cmb.outbound_model_name,
    cmb.available
FROM credential_model_bindings cmb
WHERE cmb.credential_id IN (
    SELECT credential_id 
    FROM request_logs 
    WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522'
)
AND cmb.raw_model_name = 'minimax-m3';
```

**Note**: Volcano Ark providers require `endpoint ID` (outbound_model_name). See `internal/probeutil/endpoint_id.go`:
- Volcano Ark error: `InvalidEndpointOrModel`
- If endpoint ID is missing, the API might return incomplete responses

### Hypothesis 3: Network Interruption During Streaming
**Theory**: Connection was interrupted before tools data was fully streamed.

**Evidence to gather**:
- Check if `success=false` in request_logs
- Check latency_ms - abnormally short suggests premature termination
- Check chunk_count in stream_capture_audit
- Look for timeout or connection errors in logs

**Diagnostic**:
```sql
SELECT 
    request_id,
    success,
    latency_ms,
    error_code,
    error_message
FROM request_logs
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522';
```

Expected latency for minimax-m3 with tools: typically > 1000ms
If latency < 500ms and success=true, suspect incomplete response.

### Hypothesis 4: Tools Parsing Error
**Theory**: Tools were present in the response but failed to parse correctly.

**Evidence to gather**:
- Check for JSON parsing errors in logs
- Check if response format was unexpected
- Look for "invalid JSON" or "unmarshal error" in logs

**Log search**:
```bash
sudo journalctl -u llm-gateway --since '1 hour ago' | grep '0becce7ba3e9e5dcce83e9153d48b522' | grep -i 'parse\|unmarshal\|json\|invalid'
```

## Phase 3: Comparison Analysis

### Compare with Working Request

Find a successful minimax-m3 request with tools:
```sql
-- Find recent successful minimax-m3 requests with tools
SELECT 
    request_id,
    provider_id,
    credential_id,
    success,
    latency_ms,
    created_at
FROM request_logs
WHERE client_model = 'minimax-m3'
AND success = true
AND created_at > NOW() - INTERVAL '1 day'
AND request_body::jsonb->'tools' IS NOT NULL
ORDER BY created_at DESC
LIMIT 10;

-- Compare request structure
SELECT 
    'broken' as label,
    request_id,
    provider_id,
    jsonb_array_length(request_body::jsonb->'tools') as tool_count,
    request_body::jsonb->'stream' as stream_mode
FROM request_logs
WHERE request_id = '0becce7ba3e9e5dcce83e9153d48b522'

UNION ALL

SELECT 
    'working' as label,
    request_id,
    provider_id,
    jsonb_array_length(request_body::jsonb->'tools') as tool_count,
    request_body::jsonb->'stream' as stream_mode
FROM request_logs
WHERE client_model = 'minimax-m3'
AND success = true
AND request_body::jsonb->'tools' IS NOT NULL
ORDER BY created_at DESC
LIMIT 1;
```

## Phase 4: Specific Checks

### Check Provider Catalog Configuration

```sql
-- Verify provider catalog settings
SELECT 
    catalog_code,
    display_name,
    provider_class,
    protocol,
    base_url,
    strip_private_fields
FROM provider_catalog
WHERE catalog_code IN ('volcano-tokenplan', 'volcengine-coding', 'doubao');

-- Check if strip logic is enabled
SELECT 
    p.provider_id,
    p.provider_name,
    p.catalog_code,
    pc.strip_private_fields
FROM providers p
JOIN provider_catalog pc ON p.catalog_code = pc.catalog_code
WHERE p.provider_id IN (29, 34, 35);
```

### Check Credential Model Bindings

```sql
-- Check minimax-m3 bindings for provider 34
SELECT 
    cmb.credential_id,
    c.credential_name,
    cmb.raw_model_name,
    cmb.outbound_model_name,
    cmb.available,
    p.provider_name,
    p.catalog_code
FROM credential_model_bindings cmb
JOIN credentials c ON cmb.credential_id = c.credential_id
JOIN providers p ON c.provider_id = p.provider_id
WHERE p.provider_id = 34
AND cmb.raw_model_name = 'minimax-m3'
AND cmb.available = true;
```

## Quick Diagnostic Script

Save this as `diagnose_request.sh` on server 154:

```bash
#!/bin/bash
REQUEST_ID="0becce7ba3e9e5dcce83e9153d48b522"

echo "=== Request Logs ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT request_id, client_model, provider_id, success, error_code, latency_ms, created_at 
FROM request_logs 
WHERE request_id = '$REQUEST_ID';
\""

echo ""
echo "=== Tools in Request ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT jsonb_pretty(request_body::jsonb->'tools') 
FROM request_logs 
WHERE request_id = '$REQUEST_ID';
\""

echo ""
echo "=== Stream Capture ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT chunk_count, finish_reason, has_tool_calls 
FROM stream_capture_audit 
WHERE request_id = '$REQUEST_ID';
\""

echo ""
echo "=== Gateway Logs ==="
sudo journalctl -u llm-gateway --since '2 hours ago' --no-pager | grep "$REQUEST_ID" | head -50

echo ""
echo "=== Provider Configuration ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT p.provider_id, p.provider_name, p.catalog_code, cmb.outbound_model_name
FROM request_logs rl
JOIN credentials c ON rl.credential_id = c.credential_id
JOIN providers p ON c.provider_id = p.provider_id
LEFT JOIN credential_model_bindings cmb ON c.credential_id = cmb.credential_id AND cmb.raw_model_name = 'minimax-m3'
WHERE rl.request_id = '$REQUEST_ID';
\""
```

## Expected Findings

Based on the systematic investigation, you should be able to determine:

1. **If tools were in the request**: Check `request_body->tools` in request_logs
2. **If the upstream responded**: Check success, latency_ms, chunk_count
3. **If tools were in the response**: Check stream_capture_audit
4. **If stripping removed tools**: Check catalog_code and strip logic
5. **If endpoint ID is missing**: Check credential_model_bindings.outbound_model_name

## Next Steps Based on Findings

### If tools missing from upstream response:
- Check endpoint ID configuration
- Test with direct API call to Volcano Ark
- Compare with working provider

### If tools stripped by gateway:
- Fix `IsDoubaoCatalog` to exclude `volcano-tokenplan`
- Or adjust strip logic to preserve tools field

### If network interruption:
- Check timeout settings
- Review connection stability
- Check for proxy/firewall issues

### If parsing error:
- Check response format compatibility
- Review normalizer logic
- Add error handling

---

**Created**: 2026-07-25
**Request ID**: 0becce7ba3e9e5dcce83e9153d48b522
**Status**: Awaiting server access to gather evidence
