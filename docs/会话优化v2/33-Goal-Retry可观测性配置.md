# Goal Retry Observability Dashboard

This dashboard provides comprehensive monitoring of Goal mode retry behavior.

## Metrics Available

### 1. Retry Attempts Counter
```promql
llmgw_goal_retry_attempts_total{tenant_id="$tenant", cost_mode="$mode", outcome="$outcome"}
```

**Labels:**
- `tenant_id`: Tenant identifier
- `cost_mode`: minimal, balanced, aggressive
- `outcome`: success, exhausted, cancelled, timeout, error

**Usage:**
```promql
# Retry rate by tenant
rate(llmgw_goal_retry_attempts_total[5m])

# Success rate
rate(llmgw_goal_retry_attempts_total{outcome="success"}[5m]) 
  / 
rate(llmgw_goal_retry_attempts_total[5m])
```

### 2. Retry Duration Histogram
```promql
llmgw_goal_retry_duration_seconds{tenant_id="$tenant", cost_mode="$mode"}
```

**Usage:**
```promql
# 95th percentile retry duration
histogram_quantile(0.95, 
  rate(llmgw_goal_retry_duration_seconds_bucket[5m])
)

# Average retry duration by cost mode
rate(llmgw_goal_retry_duration_seconds_sum[5m])
  /
rate(llmgw_goal_retry_duration_seconds_count[5m])
```

### 3. Retry Count Distribution
```promql
llmgw_goal_retry_count{tenant_id="$tenant", cost_mode="$mode"}
```

**Usage:**
```promql
# Average retries per request
rate(llmgw_goal_retry_count_sum[5m])
  /
rate(llmgw_goal_retry_count_count[5m])

# 99th percentile retry count
histogram_quantile(0.99, 
  rate(llmgw_goal_retry_count_bucket[5m])
)
```

### 4. Policy Resolution Counter
```promql
llmgw_goal_retry_policy_resolution_total{tenant_id="$tenant", cost_mode="$mode", source="$source"}
```

**Labels:**
- `source`: resolver, fallback

**Usage:**
```promql
# Fallback rate (should be low)
rate(llmgw_goal_retry_policy_resolution_total{source="fallback"}[5m])
  /
rate(llmgw_goal_retry_policy_resolution_total[5m])
```

### 5. Persistence Counter
```promql
llmgw_goal_retry_count_persistence_total{tenant_id="$tenant", status="$status"}
```

**Labels:**
- `status`: success, failure, skipped

**Usage:**
```promql
# Persistence failure rate (should be near zero)
rate(llmgw_goal_retry_count_persistence_total{status="failure"}[5m])
  /
rate(llmgw_goal_retry_count_persistence_total[5m])
```

### 6. Active Retries Gauge
```promql
llmgw_goal_active_retries{tenant_id="$tenant"}
```

**Usage:**
```promql
# Current active retry count by tenant
sum by (tenant_id) (llmgw_goal_active_retries)
```

---

## Grafana Dashboard JSON

Save this as `goal-retry-dashboard.json`:

```json
{
  "dashboard": {
    "title": "Goal Retry Observability",
    "tags": ["goal", "retry", "session-v2"],
    "timezone": "browser",
    "templating": {
      "list": [
        {
          "name": "tenant",
          "type": "query",
          "query": "label_values(llmgw_goal_retry_attempts_total, tenant_id)"
        },
        {
          "name": "cost_mode",
          "type": "custom",
          "options": ["minimal", "balanced", "aggressive"]
        }
      ]
    },
    "panels": [
      {
        "title": "Retry Rate by Outcome",
        "type": "graph",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_attempts_total{tenant_id=~\"$tenant\"}[5m])"
          }
        ],
        "legend": { "show": true }
      },
      {
        "title": "Retry Success Rate",
        "type": "graph",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_attempts_total{tenant_id=~\"$tenant\", outcome=\"success\"}[5m]) / rate(llmgw_goal_retry_attempts_total{tenant_id=~\"$tenant\"}[5m])"
          }
        ]
      },
      {
        "title": "P95 Retry Duration",
        "type": "graph",
        "targets": [
          {
            "expr": "histogram_quantile(0.95, rate(llmgw_goal_retry_duration_seconds_bucket{tenant_id=~\"$tenant\"}[5m]))"
          }
        ]
      },
      {
        "title": "Average Retries Per Request",
        "type": "graph",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_count_sum{tenant_id=~\"$tenant\"}[5m]) / rate(llmgw_goal_retry_count_count{tenant_id=~\"$tenant\"}[5m])"
          }
        ]
      },
      {
        "title": "Cost Mode Comparison",
        "type": "graph",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_attempts_total{tenant_id=~\"$tenant\"}[5m])",
            "legendFormat": "{{cost_mode}}"
          }
        ]
      },
      {
        "title": "Active Retries",
        "type": "graph",
        "targets": [
          {
            "expr": "sum by (tenant_id) (llmgw_goal_active_retries{tenant_id=~\"$tenant\"})"
          }
        ]
      },
      {
        "title": "Persistence Health",
        "type": "stat",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_count_persistence_total{status=\"failure\"}[5m]) / rate(llmgw_goal_retry_count_persistence_total[5m])"
          }
        ],
        "thresholds": [
          { "value": 0.01, "color": "green" },
          { "value": 0.05, "color": "yellow" },
          { "value": 0.1, "color": "red" }
        ]
      },
      {
        "title": "Policy Fallback Rate",
        "type": "stat",
        "targets": [
          {
            "expr": "rate(llmgw_goal_retry_policy_resolution_total{source=\"fallback\"}[5m]) / rate(llmgw_goal_retry_policy_resolution_total[5m])"
          }
        ],
        "thresholds": [
          { "value": 0.01, "color": "green" },
          { "value": 0.1, "color": "yellow" },
          { "value": 0.3, "color": "red" }
        ]
      }
    ]
  }
}
```

---

## Alert Rules

Save this as `goal-retry-alerts.yml`:

```yaml
groups:
  - name: goal_retry
    interval: 1m
    rules:
      - alert: GoalRetryExhaustionHigh
        expr: |
          rate(llmgw_goal_retry_attempts_total{outcome="exhausted"}[5m])
            /
          rate(llmgw_goal_retry_attempts_total[5m])
          > 0.1
        for: 5m
        labels:
          severity: warning
          component: goal_retry
        annotations:
          summary: "High retry exhaustion rate for tenant {{ $labels.tenant_id }}"
          description: "{{ $value | humanizePercentage }} of requests exhausted retries (threshold: 10%)"

      - alert: GoalRetryPersistenceFailure
        expr: |
          rate(llmgw_goal_retry_count_persistence_total{status="failure"}[5m])
            /
          rate(llmgw_goal_retry_count_persistence_total[5m])
          > 0.05
        for: 3m
        labels:
          severity: warning
          component: goal_retry
        annotations:
          summary: "Goal retry count persistence failing for tenant {{ $labels.tenant_id }}"
          description: "{{ $value | humanizePercentage }} persistence failures (threshold: 5%)"

      - alert: GoalRetryDurationHigh
        expr: |
          histogram_quantile(0.95, 
            rate(llmgw_goal_retry_duration_seconds_bucket[5m])
          ) > 60
        for: 5m
        labels:
          severity: info
          component: goal_retry
        annotations:
          summary: "High retry duration for tenant {{ $labels.tenant_id }}"
          description: "P95 retry duration is {{ $value }}s (threshold: 60s)"

      - alert: GoalRetryPolicyFallbackHigh
        expr: |
          rate(llmgw_goal_retry_policy_resolution_total{source="fallback"}[5m])
            /
          rate(llmgw_goal_retry_policy_resolution_total[5m])
          > 0.1
        for: 5m
        labels:
          severity: info
          component: goal_retry
        annotations:
          summary: "High policy fallback rate"
          description: "{{ $value | humanizePercentage }} of policies using fallback (threshold: 10%)"

      - alert: GoalActiveRetriesStuck
        expr: |
          llmgw_goal_active_retries > 100
        for: 10m
        labels:
          severity: critical
          component: goal_retry
        annotations:
          summary: "Unusually high active retries for tenant {{ $labels.tenant_id }}"
          description: "{{ $value }} active retry loops (may indicate stuck requests)"
```

---

## Deployment Verification Queries

After deploying metrics, run these queries to verify:

```bash
# 1. Verify metrics are being collected
curl -s http://localhost:9090/api/v1/label/__name__/values | jq '.data[]' | grep llmgw_goal_retry

# 2. Check if any retries have occurred
curl -s 'http://localhost:9090/api/v1/query?query=llmgw_goal_retry_attempts_total' | jq .

# 3. Verify all labels are present
curl -s 'http://localhost:9090/api/v1/label/tenant_id/values' | jq .
curl -s 'http://localhost:9090/api/v1/label/cost_mode/values' | jq .
curl -s 'http://localhost:9090/api/v1/label/outcome/values' | jq .

# 4. Check active retries gauge
curl -s 'http://localhost:9090/api/v1/query?query=llmgw_goal_active_retries' | jq .
```

---

## Quick Analysis Queries

```promql
# Top 5 tenants by retry rate
topk(5, 
  rate(llmgw_goal_retry_attempts_total[5m])
)

# Retry exhaustion by cost mode
sum by (cost_mode) (
  rate(llmgw_goal_retry_attempts_total{outcome="exhausted"}[5m])
)

# Compare minimal vs aggressive effectiveness
rate(llmgw_goal_retry_attempts_total{cost_mode="minimal", outcome="success"}[5m])
  /
rate(llmgw_goal_retry_attempts_total{cost_mode="aggressive", outcome="success"}[5m])

# Persistence health check
1 - (
  rate(llmgw_goal_retry_count_persistence_total{status="failure"}[5m])
    /
  rate(llmgw_goal_retry_count_persistence_total[5m])
)
```

---

## Troubleshooting

### No metrics appearing
1. Check if Goal mode is enabled: `goal.enabled=true`
2. Verify resolver is wired: Check logs for `goal_retry_policy_resolved`
3. Confirm requests are actually using Goal mode

### Metrics stuck at zero
1. Verify retry-eligible errors are occurring
2. Check `max_retry_count` setting is > 0
3. Review `goal.retry_on_error` is true

### High persistence failures
1. Check database connectivity
2. Review `goal_sessions` table permissions
3. Verify `gwSessionID` is being set correctly

---

## Related Documentation

- [31-当前实现基线与修正决策.md](./31-当前实现基线与修正决策.md) - Implementation baseline
- [32-审计发现与改进计划.md](./32-审计发现与改进计划.md) - Audit findings
- [18-Goal模式成本控制与分级方案.md](./18-Goal模式成本控制与分级方案.md) - Cost mode design
