# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

## 2026-08-18T18:59:33Z — deploy 154 build_seq 1622 (884ee9e1)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 540 | `540_stats_event_inbox_consumer.sql` | `7241ab1a5e458e883d04031d97890049115f2e42fa5232fff94d48af8d7cfe0a` | applied+verified |

## 2026-08-19T07:12:55Z — deploy 245 build_seq 1626 (cfe53228)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 542 | `542_request_logs_token_band.sql` | `2ed9d3e8c5b555774a6bb97757c8be73fa32e45a420c8118af036e387b2d079b` | applied+verified |

## 2026-08-19T10:11:23Z — deploy 154 build_seq 1629 (b310b700)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 543 | `543_request_logs_discard_events.sql` | `6fe37eb4f0dd5cc75f400376473471b5f2b6f5f8edcdf2d92a1f71d291b18fa8` | applied+verified |

## 2026-08-19T10:11:23Z — deploy 154 build_seq 1629 (b310b700)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 544 | `544_stats_adjustments_alignment.sql` | `9c06b5ac0ee9e8ad04c9517963399518ff9118b14f3619a67475641e8058ea1e` | applied+verified |

## 2026-08-20T02:37:33Z — deploy 154 build_seq 1631 (manual fix)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 545 | `545_stats_reconciliation_phantom_resolution.sql` | `6f2cbdefb62fefa17ea5d9d5238b5f2062720fd636a97d60ebda9a862579a993` | applied+verified |
| 546 | `546_stats_reconciliation_diffs_unique.sql` | `57a05c88937b` | applied+verified |
## 2026-08-19T18:39:56Z — deploy 154 build_seq 1632 (0eec1b62)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 547 | `547_session_project_attribution.sql` | `359353c4155d1c4331ef56356d1cae8e408b7ece01c9e1ef95b8adb9dbda16a2` | applied+verified |

