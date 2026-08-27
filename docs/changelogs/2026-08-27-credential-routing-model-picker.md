# Credential Routing Model Picker Completeness

## Summary

The credential routing model picker treated its 20-item limit as a combined
limit for policy-featured and usage-popular models. A long feature list could
therefore hide current popular models, despite the UI contract requiring all
featured models plus current, real-user popularity.

## Changes

- Keep every `routing_policy.featured_models` entry in the picker response.
- Apply the popularity limit only to usage-derived entries.
- Remove Redis live-lane entries because lane cardinality includes probes and
  idle markers.
- Exclude rows tagged `probe` from the `request_logs_hot` popularity fallback.
- Retain the tenant-scoped recent-use Redis ZSET, whose write path already
  rejects probe traffic.

## Verification

- `go test ./admin -run 'Test(PopularModelsHotSQL|QueryPopularModels|RecentlyUsedPopularModels|RecordRecentlyUsedModel)' -count=1`
- `go build ./...`
