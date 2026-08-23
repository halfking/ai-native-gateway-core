# 2026-08-23 — Live stream inflight protect env wiring

## TL;DR

Wire `LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS` at gateway startup to override the default 2h in_progress protection window for selective lane trim.

## Verification

```bash
go test ./admin -run ConfigureLiveStreamInflight -count=1
```
