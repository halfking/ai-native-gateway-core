# DB Changelog

部署时在 **切换前** 于 252 PG 应用的 `sql/migrations/startup/` 变更。
其它环境请按时间倒序手工同步，或使用 `deploy-seamless.sh` 自动 apply。

---

## 2026-07-17 — 请求链路追踪系统 (Redis暂存+JSONB持久化)

详见 `docs/deployment/2026-07-17-request-trace-system.md`。

| Migration | File |
|-----------|------|
| 420 | `420_request_logs_trace_events.sql` |

---

## 2026-07-15 — download publish runs

| Migration | File |
|-----------|------|
| 407 | `407_download_publish_runs.sql` |

---

## 2026-07-14T19:57:33Z — deploy 245 build_seq 1030 (4c445c1d)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |

## 2026-07-14T20:02:28Z — deploy 245 build_seq 1031 (506fcfaa)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-14T20:04:10Z — deploy 245 build_seq 1032 (506fcfaa)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:31:35Z — deploy 245 build_seq 1033 (9b98b1e7)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:32:05Z — deploy 245 build_seq 1034 (9b98b1e7)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:32:59Z — deploy 245 build_seq 1035 (9b98b1e7)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:35:45Z — deploy 245 build_seq 1036 (54fd632f)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:36:07Z — deploy 245 build_seq 1037 (54fd632f)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:37:09Z — deploy 245 build_seq 1038 (54fd632f)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:39:24Z — deploy 245 build_seq 1039 (54fd632f)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T02:50:12Z — deploy 154 build_seq 1042 (54fd632f)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T03:06:21Z — deploy 245 build_seq 1044 (93404bc3)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |

## 2026-07-15T03:34:16Z — deploy 245 build_seq 1047 (ddee0470)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |
| 407 | `407_download_publish_runs.sql` |
| 410 | `410_ip_blocklist.sql` |
| 411 | `411_ops_node_registrations.sql` |

## 2026-07-15T03:35:45Z — deploy 245 build_seq 1048 (ddee0470)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |
| 407 | `407_download_publish_runs.sql` |
| 410 | `410_ip_blocklist.sql` |
| 411 | `411_ops_node_registrations.sql` |

## 2026-07-15T05:47:12Z — deploy 245 build_seq 1049 (5c532d36)

| Migration | File |
|-----------|------|
| 403 | `403_runtime_alert_events.sql` |
| 404 | `404_partition_autovacuum_analyze.sql` |
| 405 | `405_glm52_promote_per_token_to_token_plan.sql` |
| 406 | `406_recent_success_rate_read_hot.sql` |
| 407 | `407_download_publish_runs.sql` |
| 410 | `410_ip_blocklist.sql` |
| 411 | `411_ops_node_registrations.sql` |

## 2026-07-15T15:24:40Z — deploy 245 build_seq 1063 (db7f4d68)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
## 2026-07-15T19:40:03Z — deploy 245 build_seq 1072 (2f2f8df0)

| Migration | File |
|-----------|------|
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T02:26:28Z — deploy 245 build_seq 1073 (914503d6)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T02:31:47Z — deploy 245 build_seq 1074 (914503d6)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T02:40:54Z — deploy 245 build_seq 1075 (f7c4a591)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T03:25:23Z — deploy 245 build_seq 1076 (1960d94f)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T03:35:26Z — deploy 245 build_seq 1077 (29cd745c)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T03:53:25Z — deploy 245 build_seq 1080 (93fea799)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T04:22:43Z — deploy 245 build_seq 1084 (757cdef5)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T04:33:42Z — deploy 245 build_seq 1085 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T04:52:32Z — deploy 154 build_seq 1087 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:07:44Z — deploy 245 build_seq 1088 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:08:52Z — deploy 154 build_seq 1089 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:19:43Z — deploy 245 build_seq 1090 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:21:54Z — deploy 154 build_seq 1091 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:31:51Z — deploy 245 build_seq 1092 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T05:32:22Z — deploy 154 build_seq 1093 (93e556e3)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T06:24:01Z — deploy 245 build_seq 1094 (1d7d6b2c)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T06:25:01Z — deploy 154 build_seq 1095 (1d7d6b2c)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T06:56:17Z — deploy 154 build_seq 1096 (d385183e)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T06:58:21Z — deploy 245 build_seq 1097 (d385183e)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T07:15:23Z — deploy 245 build_seq 1098 (cf77195a)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T07:21:20Z — deploy 245 build_seq 1099 (4a8ab149)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T07:23:45Z — deploy 154 build_seq 1100 (4a8ab149)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T08:33:13Z — deploy 245 build_seq 1101 (2c63da4c)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T08:47:44Z — deploy 154 build_seq 1102 (2c63da4c)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T10:52:49Z — deploy 245 build_seq 1103 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T11:48:42Z — deploy 245 build_seq 1104 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |

## 2026-07-16T11:56:01Z — deploy 245 build_seq 1105 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |

## 2026-07-16T12:00:43Z — deploy 245 build_seq 1106 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |

## 2026-07-16T12:07:41Z — deploy 245 build_seq 1108 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |

## 2026-07-16T12:16:06Z — deploy 245 build_seq 1111 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |

## 2026-07-16T12:20:49Z — deploy 245 build_seq 1112 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |

## 2026-07-16T12:22:45Z — deploy 245 build_seq 1113 (f3a3ec02)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |

## 2026-07-16T14:16:56Z — deploy 245 build_seq 1114 (77bbf2e9)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |

## 2026-07-16T14:23:08Z — deploy 154 build_seq 1115 (a2cdc398)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |

## 2026-07-16T17:26:52Z — deploy 245 build_seq 1117 (1477daf2)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T17:28:26Z — deploy 154 build_seq 1118 (1477daf2)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:03:22Z — deploy 245 build_seq 1119 (c46d5b9f)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:16:48Z — deploy 154 build_seq 1120 (c46d5b9f)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:37:58Z — deploy 245 build_seq 1121 (5c3e8cb0)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:38:07Z — deploy 154 build_seq 1122 (5c3e8cb0)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:39:43Z — deploy 154 build_seq 1123 (5c3e8cb0)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-16T18:59:35Z — deploy 154 build_seq 1124 (306c1700)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |

## 2026-07-17T04:46:22Z — deploy 245 build_seq 1125 (141df8df)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |
| 421 | `421_task_default_routing.sql` |
| 422 | `422_models_canonical_complexity.sql` |
| 423 | `423_approval_routing_rules_add_legacy_columns.sql` |

## 2026-07-17T06:43:54Z — deploy 245 build_seq 1126 (5ce49a70)

| Migration | File |
|-----------|------|
| 412 | `412_rca_ts_index.sql` |
| 413 | `413_runtime_alert_events_ts_index.sql` |
| 414 | `414_drop_model_probe_runs_old.sql` |
| 415 | `415_restore_node_probe_runs.sql` |
| 416 | `416_reconcile_node_probe_bindings.sql` |
| 417 | `417_route_excludes_failed_node_probes.sql` |
| 418 | `418_rearm_node_probes_after_url_fix.sql` |
| 419 | `419_node_probe_runs_complete_fields.sql` |
| 420 | `420_request_logs_trace_events.sql` |
| 421 | `421_task_default_routing.sql` |
| 422 | `422_models_canonical_complexity.sql` |
| 423 | `423_approval_routing_rules_add_legacy_columns.sql` |

## 2026-07-17T14:19:09Z — deploy 245 build_seq 1128 (dd2d9b81)

| Migration | File |
|-----------|------|
| 426 | `426_task_type_centroids.sql` |
| 427 | `427_task_type_centroids_model.sql` |

## 2026-07-17T15:01:41Z — deploy 245 build_seq 1129 (aea52403)

| Migration | File |
|-----------|------|
| 428 | `428_recent_success_rate_probe_filters.sql` |

## 2026-07-17T17:22:33Z — deploy 245 build_seq 1138 (57a12597)

| Migration | File |
|-----------|------|
| 430 | `430_sessions_v2_schema.sql` |

## 2026-07-19T14:34:47Z — deploy 245 build_seq 1179 (2c52f1ef)

| Migration | File |
|-----------|------|
| 446 | `446_volcengine_model_aliases.sql` |
| 447 | `447_volcano_glm_outbound_mapping.sql` |

## 2026-07-19T20:21:16Z — deploy 154 build_seq 1198 (cfbd0a36)

| Migration | File |
|-----------|------|
| 449 | `449_request_logs_hot_trace_events.sql` |

## 2026-07-19T22:44:52Z — deploy 154 build_seq 1207 (3998271e)

| Migration | File |
|-----------|------|
| 450 | `450_request_stage_events_tenant.sql` |

## 2026-07-21T16:18:13Z — deploy 245 build_seq 1262 (97259716)

| Migration | File |
|-----------|------|
| 453 | `453_ursm_v2_node_snapshot_min.sql` |

## 2026-07-22T20:08:46Z — deploy 154 build_seq 1311 (498d9f10)

| Migration | File |
|-----------|------|
| 454 | `454_response_format_anomalies.sql` |

## 2026-07-23T04:35:41Z — deploy 245 build_seq 1342 (7536a176)

| Migration | File |
|-----------|------|
| 455 | `455_request_id_unique_for_hot_tables.sql` |

## 2026-07-24T09:10:16Z — deploy 245 build_seq 1365 (2c8ccc61)

| Migration | File |
|-----------|------|
| 457 | `457_session_v2_owner_filter.sql` |

## 2026-07-26T21:15:42Z — deploy 245 build_seq 1404 (73e4cff8)

| Migration | File |
|-----------|------|
| 458 | `458_request_logs_canonical_model.sql` |

## 2026-07-26T21:50:16Z — deploy 245 build_seq 1408 (7edaf19a)

| Migration | File |
|-----------|------|
| 459 | `459_request_logs_view_client_perception.sql` |
| 460 | `460_v_routable_credential_models_periodic_exhausted.sql` |

## 2026-07-28T08:52:33Z — deploy 245 build_seq 1414 (33a68312)

| Migration | File |
|-----------|------|
| 462 | `462_model_integrity_events.sql` |

## 2026-08-04T07:01:53Z — deploy 245 build_seq 1438 (4e3ae015)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 464 | `464_session_turns_aggregate_claim.sql` | `9ea33e5bd1eb82a0eaf2e7c0c5cc57785063aa7c834eb255a3e236656bd53b28` | applied+verified |

## 2026-08-06T02:09:43Z — deploy 245 build_seq 1454 (e7635431)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 465 | `465_session_titles_pkey.sql` | `99c2776ec07168c52188e3b9513cd38a7499833e15d98103baea853c1dd32e85` | applied+verified |

## 2026-08-06T02:52:19Z — deploy 245 build_seq 1460 (fc395aac)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 466 | `466_relax_compression_parent_check.sql` | `d1b7fbd10e77715017bc54462e463e76b83ff8f25e1e1f61be7828255c8401f6` | applied+verified |

## 2026-08-07T04:48:12Z — deploy 245 build_seq 1471 (a7b840e3)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 467 | `467_sessions_title_user_tags.sql` | `506c155e773dbc30df2568f52b86922bcf5637354cd5091a29ee7e7f6fd89954` | applied+verified |
| 468 | `468_v_suspicious_probe_targets_admin_protected.sql` | `ca1bf0753e232736f1c41973c69f14a17e4e9d201aeff381880e3a6284fc43d6` | applied+verified |
| 469 | `469_context_window_override.sql` | `b540ad02c5f23368a96a50fdb0cff3c786e676618d58cd5fd3100a9352bb8b0b` | applied+verified |
| 470 | `470_cache_metrics.sql` | `f07644b71a6f62652b0ada201afb4125b8fe6c5398172d110e960722d7372846` | applied+verified |

## 2026-08-07T08:15:14Z — deploy 245 build_seq 1473 (006e4901)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 472 | `472_cache_metrics_partitions.sql` | `a2667ef1a5436ed01272a4d8260840e486d6c00ed1e4b6552b27e48887e95053` | applied+verified |

## 2026-08-07T08:45:23Z — deploy 245 build_seq 1472 (26478d73)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 473 | `473_partition_precreate_2026_09_10.sql` | `2adc06f310153fd514f665b8c85c3dd9c55e7c4423c5fa096d938592e1f4212b` | applied+verified |
| 474 | `474_session_turns_attachment_indexes_and_constraint.sql` | `07c9b6e62a57f2ef7b7db27f447c1667734792a4a9029c3de38c086c434f8452` | applied+verified |

## 2026-08-07T19:30:00Z — audit fix: partition automation (24h audit follow-up)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 475 | `475_restore_missing_ensure_partition_functions.sql` | `355f9b3b82e68ccce08e89e681ad7f121ef4e3c6fb47ea4b0406a2021cb56c1a` | pending (this commit) |

**背景**: 24h 审计 (`AUDIT_REPORT_24H_20260807.md`) 发现：
1. `bg.PartitionManager.ensureSpecs()` 只覆盖 6 张表，14+ 张 RANGE 分区表无自动化
2. 迁移 473 只是补到 2026_10 的一次性补丁，2026-11-01 后会再次因「no partition of relation found for row」全面写入失败
3. 进一步调查发现：迁移 334/335/382/383 的 `ensure_*_partition()` 函数从未进入生产库（与 431/432 同类的 silent skip 问题），导致即使把函数名加入 Go 端也会因 "function does not exist" 报错

**修复**:
- 迁移 475 用 `CREATE OR REPLACE FUNCTION` 幂等重建 5 个生产缺失的函数：
  - `ensure_credit_ledger_partition(timestamptz)` ← 334
  - `ensure_tool_usage_stats_partition(timestamptz)` ← 335
  - `ensure_session_module_executions_partition(date)` ← 382
  - `ensure_dashboard_events_partition(date)` ← 383
  - `ensure_cache_metrics_partition(date)` ← 新建
- 迁移末尾 `DO $$` 块立即为当月 + 下月执行 `PERFORM ensure_*()`，无需等待 PartitionManager 下次 tick
- Go 端 `bg/partition_manager.go`：
  - `archiveSpec` 新增 `argExpr` 字段支持 `$1::date` cast（pgx 默认发 timestamptz，date 签名函数需显式转换）
  - `ensureSpecs()` 新增 6 个条目 → 覆盖 11 张物理表（12 个逻辑表，`ensure_sessions_v2_partitions` 一次覆盖 sessions/session_turns/session_bodies）

**验证**:
- ✅ `go build ./...` 编译通过
- ✅ `go test ./bg/ -v` 全部 PASS
- ✅ Docker Postgres 16 上 475 up 验证：5 函数 + 当月/下月分区正确创建
- ✅ 幂等性：二次运行返回 "already exists"，无重复建表
- ✅ down 文件对称：删除 5 函数，不动已建分区（防数据丢失）

## 2026-08-08T04:14:33Z — deploy 245 build_seq 1473 (da253d3d)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 476 | `476_session_v2_tenant_unique_keys.sql` | `6ab1179688a27f11f28fe2696fcbbcf267e28ad3df2248d92c659fec14b86c42` | applied+verified |

> **2026-08-08 24h 审计笔记**：commit `c13ee3c6a4` 的 commit message
> 把 476 写成 `476_routes_incidents_audit_safety`，与实际文件名
> `476_session_v2_tenant_unique_keys.sql` 不一致 —— 上面表格才是
> 真实文件名（事务级 tenant 唯一键）。运维按 commit message 找迁移
> 会找不到，必须按 commit SHA 反查。`c13ee3c6a4` 仅是回填 db-changelog
> 条目的 doc commit，没有改迁移文件本身，因此 sha256 仍是
> `6ab117...`。`c13ee3c6a4` 提交历史不可改，但请把本 note 视为权威
> 文件名映射。

## 2026-08-10T16:37:43Z — deploy 245 build_seq 1489 (b0f29dc1)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 477 | `477_auto_route_v6_defaults.sql` | `05966190dcb0fb8c9e2a1cbecb9e97b7e9dd3561d3615acf3f8934598e4b8772` | applied+verified |

## 2026-08-10T18:59:44Z — deploy 245 build_seq 1489 (11e553dc)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 478 | `478_auto_route_affinity.sql` | `520c3e510b77200e7679ebe10574c7d10f4600bfc108773431a0a462907d15b5` | applied+verified |

## 2026-08-10T18:59:48Z — deploy 154 build_seq 1490 (11e553dc)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 478 | `478_auto_route_affinity.sql` | `520c3e510b77200e7679ebe10574c7d10f4600bfc108773431a0a462907d15b5` | applied+verified |

## 2026-08-11T16:30:13Z — deploy 245 build_seq 1491 (8a788d30)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 479 | `479_concurrency_mode.sql` | `066445e9d55602562bcbc69720eed2121c676ca8b1bbd1e8d6a0210f0793408e` | applied+verified |

## 2026-08-12T19:22:23Z — deploy 245 build_seq 1505 (456875ed)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 487 | `487_request_logs_add_system_fingerprint.sql` | `e5ade1908e2c93998bf819c7e9adfd3107097adf8de4f1fc43eaac172353e5e6` | applied+verified |

## 2026-08-13T15:34:22Z — deploy 245 build_seq 1505 (83b40b7d)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 490 | `490_credential_probe_queue_runtime_columns.sql` | `cfda9da5259662fdc34249bf345ec33480ea0e70aa4541a912614b3d84688971` | applied+verified |
| 510 | `510_request_type.sql` | `d663b0915a0db9b82acec08abdcff8a6f6ae97ad64d681e0788f7b998d8f7645` | applied+verified |
| 511 | `511_state_transitions_table.sql` | `f4c9cc150bb2cc39a22639794fd43fdc780da89c270e252a8ab5f9fe8e6bd199` | applied+verified |

## 2026-08-14T04:45:26Z — deploy 245 build_seq 1511 (ff3d6449)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 513 | `513_schema_unification_and_session_turns_dual_write.sql` | `63dcac48097f0f69ee72ac1dc95b50de7190d3accc2cb5a728ce6eca0c2e7e98` | applied on 245; 252 rehearsal and RLS verification pending |

> 245 deploy output confirmed the migration transaction completed and the gateway DB readiness check returned `background-tasks=401`. This is not evidence that the required 252 Migration 513 up/down rehearsal or `test_511_rls.test.sql` has passed; track those P0 checks in `docs/会话优化v3/TASK-CLOSURE-20260814.md` before marking the migration verified.

## 2026-08-14T14:52:38Z — deploy 245 build_seq 1522 (7be7f5be)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 514 | `514_model_probe_watchdog_index.sql` | `fbcb9719af0960199e5375140d99c65378b1772999bbc0f1636947cbedc33790` | applied+verified |

## 2026-08-15T04:48:25Z — deploy 245 build_seq 1538 (186ae04c)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 515 | `515_state_transitions_seq_unique.sql` | `3f0f328f3575a3445b037750518f4bf68037327f0a7c1d3bc455e693b37f057a` | applied+verified |

## 2026-08-15T11:14:38Z — deploy 245 build_seq 1538 (f7fadd17)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 516 | `516_durable_llm_tasks.sql` | `ab38d456e680ed2dfdc778a867a6137ad40a1ffa9bca76a13e3c524cc1955608` | applied+verified |
