# 2026-08-26 Popular Models Follow-up

## Goal
- Complete the popular-model audit follow-ups without mixing tenant data.

## Changes
- Partition recently-used model Redis ZSETs by tenant and read them only for the caller's tenant scope.
- Filter hot-table usage by `tenant_id` and key the available-model response cache by tenant scope.
- Add `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS`; invalid values fall back to seven days.
- Stop before the SQL fallback when policy and Redis results already satisfy the picker limit.
- Add the pre-warmed `llmgw_live_stream_tile_overlay_db_lookup_total{outcome}` metric.

## Evidence
- `go test ./admin ./metrics -count=1` passed.
- `go vet ./...` passed.
- `go test ./...` had one timing-sensitive failure in `domains/streaming:TestCheckGatewayRateLimit_QueuedBeyondBudgetFailsFast`; its isolated rerun passed.
- `scripts/govulncheck.sh` reported eight reachable standard-library vulnerabilities from the local Go 1.26.4 toolchain; all fixes require Go 1.26.5 or 1.26.6 and are outside this change.

## Scope
- The main worktree's `domains/streaming/request_log_pipeline_test.go` modification was preserved and never copied into this isolated worktree.
- No database migration, deployment, or environment secret change was made.
