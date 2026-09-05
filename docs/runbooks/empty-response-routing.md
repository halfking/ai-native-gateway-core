# Empty-response routing

## Scope and behavior

Both **streaming** and **non-streaming** empty responses are classified as `empty_response` and
trigger immediate candidate failover before any client-visible content is committed.

### Streaming detection

`empty_stream_no_content` and `early_empty_detection` are classified as `empty_response`.

### Non-streaming detection (2026-08-27)

The executor detects a semantically empty 2xx response body before `WriteHeader`:

- **OpenAI Chat** (`executor_chat.go`): `choices` missing/empty, or `choices[0].message` has no
  `content`, `reasoning_content`, or `tool_calls`.
- **Anthropic Messages** (`executor_anthropic.go`): `content` missing/empty, or all blocks are
  empty `text`/`thinking` (a `tool_use` block, even with empty input, is not empty; a `thinking`
  block with a `signature` is not empty).

A non-stream empty response returns a bare `*upstream.Error` with `KindEmptyResponse` (NOT wrapped
in `retryableError`) so the outer candidate loop fails over immediately instead of retrying the
same credential with backoff. The handler-side 502 ("模型未返回任何内容") remains the final fallback
when all candidates are empty.

### Routing and telemetry

Empty responses also update URSM v2 telemetry, scoped to exactly:

```text
tenant_id + credential_id + raw_model
```

The policy never uses a provider-wide or canonical-model-wide circuit key. A high empty-response
rate only increases the routing score for that one node. It does not mark a binding unavailable,
change credential availability, or open the legacy provider/credential circuit.

The default five-minute policy is deliberately conservative:

- minimum samples: `10`
- empty-response-rate threshold: `0.20`
- penalty begins only above the threshold and is a soft ordering penalty

Boot overrides are `URSM_V2_EMPTY_RESPONSE_MIN_SAMPLES` and
`URSM_V2_EMPTY_RESPONSE_RATE_THRESHOLD`. Values must be in range; an invalid explicit value
rejects startup rather than silently changing the active threshold.

## Authoritative event query

Use candidate-level failures as the empty-response numerator. A terminal `request_logs` row may
identify a later successful candidate after transparent failover, so it is not sufficient to
attribute every failed attempt.

Run this only in an authorized tenant-scoped transaction. The RLS policy reads
`app.current_tenant`; the SQL parameter and RLS context must agree.

```sql
BEGIN;
SET LOCAL app.current_tenant = $1;

SELECT
  date_trunc('hour', f.ts) AS hour,
  f.provider_id,
  f.credential_id,
  f.raw_model_name AS raw_model,
  COALESCE(f.context->>'stream_reason', 'other') AS empty_reason,
  count(*) AS empty_response_attempts
FROM public.candidate_failure_logs_with_current_month AS f
WHERE f.ts >= now() - interval '24 hours'
  AND f.tenant_id = $1
  AND f.error_kind = 'empty_response'
GROUP BY 1, 2, 3, 4, 5
ORDER BY 1 DESC, empty_response_attempts DESC;

COMMIT;
```

`empty_stream_no_content` and `early_empty_detection` are separate reasons within the same
`empty_response` total. Use the same bounded tenant/window and
`(provider_id, credential_id, raw_model_name)` dimensions for the streaming-attempt denominator.
Do not select request bodies, upstream error bodies, credential labels, or secrets in routine
investigations. Platform-wide investigations require an approved break-glass role and a separate,
reviewed query.

## URSM v2 node fields

URSM stores the following telemetry in the existing tenant/credential/raw-model node hash:

- `empty_responses_1m`, `empty_responses_5m`, `empty_responses_30m`
- `empty_response_rate_1m`, `empty_response_rate_5m`, `empty_response_rate_30m`
- the existing `samples_1m`, `samples_5m`, `samples_30m`

Counts and rates are updated atomically with the existing success-rate window and request dedup
record. Legacy window members remain readable and count as non-empty during a rolling upgrade.
