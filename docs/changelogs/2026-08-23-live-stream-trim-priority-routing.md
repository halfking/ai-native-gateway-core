# 2026-08-23 — Live stream selective trim + queue priority routing alignment

## TL;DR

- Live stream lane eviction uses post-exec selective trim so fresh `in_progress` tiles are not dropped by blind `ZRemRangeByRank`.
- Admin resolve + queue perspective cards align priority sorting with provider `COALESCE(quota_state,'ok')`.
- Audit P1 fixes: queue card order uses resolve index (not manual_priority alone); quota gate unified across Go + Vue.

## Verification

```bash
go test ./admin/ -run 'Priority|SelectiveTrim|Trim' -count=1
go test ./domains/analysis/sessionmeta/ -count=1
```
