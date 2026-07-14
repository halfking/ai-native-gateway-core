# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

## 2026-07-14T19:57:33Z — deploy 245 build_seq 1030 (4c445c1d)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |

