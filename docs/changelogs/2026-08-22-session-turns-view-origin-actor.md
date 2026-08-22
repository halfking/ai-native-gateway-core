# 2026-08-22 — Session turns view origin_actor + cursor fix

## Summary

Fixes production 500 on `GET /api/admin/sessions/{id}/turns` after waterfall「打开会话」flow.

## Changes

1. **Cursor SQL** (`admin/session_turns_tree.go`): pagination WHERE reused two placeholders for `turn_number` but bound one value.
2. **View freeze** (`561_request_logs_view_origin_actor.sql`): append `origin_actor` to `request_logs_with_current_month` (column exists on hot/parent since 341, never on VIEW).
3. **Child query**: restore `COALESCE(origin_actor, '')` SELECT after migration 561.

## Verify

```bash
go test ./admin/... -run SessionTurnsTree -count=1
# After deploy + migration 561 on 154:
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.kxpms.cn/api/admin/sessions/gw_<id>/turns?limit=20"
```

## Deploy

Requires seamless deploy to 154 (runs pending migration 561).
