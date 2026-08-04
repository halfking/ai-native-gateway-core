# Views

> 本目录包含 views 对象的 DDL 定义，从 `sql/objects/views/` 同步。

## 统计

- **文件数量**: 53
- **同步来源**: `sql/objects/views/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
credential_model_index_with_current_month.sql
credit_ledger_with_current_month.sql
customer_cost_view.sql
intent_adjustment_effectiveness.sql
intent_classification_metrics.sql
model_cost_per_task_view.sql
model_offers.sql
model_probe_runs_with_current_month.sql
output_compliance_stats_today.sql
prompt_injection_stats_today.sql
provider_error_distribution.sql
provider_health_status.sql
request_logs_bodies_progress.sql
request_logs_bodies_with_current_month.sql
request_logs_with_current_month.sql
request_wal_with_current_month.sql
routing_decision_log_with_current_month.sql
session_stats_today.sql
stage_performance_recent.sql
system_metrics_local.sql
system_metrics_recent.sql
tenant_model_policies_active.sql
tool_usage_stats_with_current_month.sql
tuning_signals_5m.sql
tuning_signals_daily.sql
upstream_5xx_distribution.sql
usage_ledger_with_current_month.sql
v_adaptive_probe_targets.sql
v_candidate_failure_logs_diagnosis.sql
v_continuation_effectiveness.sql
v_dashboard_access_stats.sql
v_dashboard_errors.sql
v_dashboard_slow_queries.sql
v_dashboard_user_activity.sql
v_format_anomaly_summary.sql
v_fp_slot_policy.sql
v_idle_credential_slots.sql
v_model_availability_timeline.sql
v_model_health_dashboard.sql
v_model_pricing_comparison.sql
v_model_priority_details.sql
v_node_switch_analysis.sql
v_probe_queue_snapshot.sql
v_probe_system_health.sql
v_recent_model_probe_failures.sql
v_routable_credential_models.sql
v_session_cache_by_model.sql
v_session_cache_stats.sql
v_sme_cache_hit_rate.sql
v_sme_failures.sql
v_sme_module_stats.sql
v_suspicious_probe_targets.sql
v_timeout_effectiveness.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/views/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/views/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/views/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/views/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
