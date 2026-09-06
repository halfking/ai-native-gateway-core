# E2E Quick Reference

## Test the E2E Path

```bash
# Send a test request
curl -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Authorization: Bearer sk-e2e-test-1781898294" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Test"}],
    "max_tokens": 50
  }'
```

Expected response:
```json
{
  "id": "chatcmpl-...",
  "model": "gpt-4",
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "echo: Test [mock=mock-provider-XX]"
    }
  }],
  "_mock_identity": "mock-provider-XX"
}
```

## Check System Status

```bash
# Mock provider containers
docker ps | grep mock-provider

# Gateway container
docker ps | grep llm-gateway

# Network connectivity
docker exec llm-gateway-local-8782 ping -c 1 mock-provider-01
```

## Database Check

```bash
docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway << 'SQL'
SELECT 
    p.code,
    cmb.available,
    cmb.routing_tier
FROM credential_model_bindings cmb
JOIN credentials c ON cmb.credential_id = c.id
JOIN providers p ON c.provider_id = p.id
WHERE p.code LIKE 'mock-provider-%';
SQL
```

## Mock Provider Container Management

```bash
# Start a mock provider
docker run -d \
  --name mock-provider-01 \
  --network shared-infra \
  -p 18080:18080 \
  -e MOCK_TOKEN=mock-provider-01 \
  -e MOCK_PORT=18080 \
  llm-mock-provider:latest

# Check health
curl http://localhost:18080/healthz

# View logs
docker logs mock-provider-01
```

## Troubleshooting

### No available provider for model 'gpt-4'

Check bindings:
```sql
UPDATE credential_model_bindings
SET available = true,
    unavailable_reason = NULL
WHERE credential_id IN (
    SELECT c.id FROM credentials c
    JOIN providers p ON c.provider_id = p.id
    WHERE p.code LIKE 'mock-provider-%'
);
```

### Gateway can't reach mock providers

Check network:
```bash
docker exec llm-gateway-local-8782 ping -c 2 mock-provider-01
```

### Credential decryption errors

Ensure credentials use proper encryption format (v1:legacy:...)

## Key Configuration

- **Gateway:** http://127.0.0.1:8782
- **Mock Providers:** http://mock-provider-{01,02,03}:18080
- **Network:** shared-infra
- **Model:** gpt-4 (canonical_id: 567164)
- **Routing Tier:** 1 (highest priority)
- **API Key:** sk-e2e-test-1781898294

## Success Criteria

✅ HTTP 200 response
✅ Valid JSON with choices array
✅ `_mock_identity` field present
✅ Response time < 2 seconds
