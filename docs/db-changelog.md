# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

## 2026-08-18T18:59:33Z — deploy 154 build_seq 1622 (884ee9e1)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 540 | `540_stats_event_inbox_consumer.sql` | `7241ab1a5e458e883d04031d97890049115f2e42fa5232fff94d48af8d7cfe0a` | applied+verified |

## 2026-08-19T10:11:23Z — deploy 154 build_seq 1629 (b310b700)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 543 | `543_request_logs_discard_events.sql` | `6fe37eb4f0dd5cc75f400376473471b5f2b6f5f8edcdf2d92a1f71d291b18fa8` | applied+verified |

