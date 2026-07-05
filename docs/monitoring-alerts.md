# LLM Gateway 监控告警配置

## Prometheus 监控指标

### 1. 模型发现成功率
```yaml
# 告警规则：模型发现失败
- alert: ModelDiscoveryFailed
  expr: |
    (
      rate(llm_gateway_model_discovery_total{status="failed"}[5m]) 
      / 
      rate(llm_gateway_model_discovery_total[5m])
    ) > 0.5
    OR
    llm_gateway_model_discovery_models_count == 0
  for: 5m
  labels:
    severity: critical
    component: model_discovery
  annotations:
    summary: "模型发现失败率过高"
    description: "最近 5 分钟模型发现失败率 {{ $value | humanizePercentage }}，或发现的模型数为 0"
    runbook_url: "https://docs/troubleshooting-guide.md#problem-2"
```

### 2. 请求成功率
```yaml
# 告警规则：请求成功率低
- alert: RequestSuccessRateLow
  expr: |
    (
      rate(llm_gateway_requests_total{status="200"}[5m])
      /
      rate(llm_gateway_requests_total[5m])
    ) < 0.95
  for: 5m
  labels:
    severity: warning
    component: routing
  annotations:
    summary: "请求成功率低于 95%"
    description: "最近 5 分钟请求成功率 {{ $value | humanizePercentage }}"
```

### 3. 无可用候选凭据
```yaml
# 告警规则：no_candidate 错误
- alert: NoCandidateErrors
  expr: |
    rate(llm_gateway_requests_total{error_code="no_candidate"}[5m]) > 1
  for: 2m
  labels:
    severity: critical
    component: routing
  annotations:
    summary: "大量 no_candidate 错误"
    description: "最近 5 分钟 no_candidate 错误率 {{ $value | humanize }}/s"
    runbook_url: "https://docs/troubleshooting-guide.md#problem-2"
```

### 4. 响应延迟过高
```yaml
# 告警规则：P95 延迟过高
- alert: HighLatency
  expr: |
    histogram_quantile(0.95, 
      rate(llm_gateway_request_duration_seconds_bucket[5m])
    ) > 5
  for: 5m
  labels:
    severity: warning
    component: performance
  annotations:
    summary: "P95 响应延迟过高"
    description: "P95 响应延迟 {{ $value | humanizeDuration }}"
```

### 5. 凭据熔断器打开
```yaml
# 告警规则：熔断器打开
- alert: CredentialCircuitOpen
  expr: |
    llm_gateway_credential_circuit_state{state="open"} > 0
  for: 10m
  labels:
    severity: warning
    component: credential
  annotations:
    summary: "凭据熔断器打开"
    description: "凭据 {{ $labels.credential_id }} ({{ $labels.label }}) 熔断器已打开超过 10 分钟"
```

---

## 数据库监控查询

### 1. 计费模式一致性检查（每小时执行）

```sql
-- 监控查询：计费模式不一致
SELECT 
    'billing_mode_mismatch' as metric_name,
    COUNT(*) as value,
    NOW() as timestamp
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token')
   OR (c.plan_type IN ('token_plan', 'code_plan', 'agent_plan') 
       AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan'));

-- 告警阈值：value > 0
```

### 2. 可用模型数量监控（每 5 分钟）

```sql
-- 监控查询：可用模型绑定数
SELECT 
    'available_model_bindings' as metric_name,
    COUNT(*) as value,
    NOW() as timestamp
FROM credential_model_bindings
WHERE available = true;

-- 告警阈值：value < 100（表示大量模型不可用）
```

### 3. 凭据健康状态（每 5 分钟）

```sql
-- 监控查询：不健康的凭据数量
SELECT 
    'unhealthy_credentials' as metric_name,
    COUNT(*) as value,
    NOW() as timestamp
FROM credentials
WHERE status = 'active'
  AND (
    circuit_state = 'open'
    OR availability_state = 'unavailable'
    OR quota_state IN ('exhausted', 'periodic_exhausted')
  );

-- 告警阈值：value > 5
```

### 4. 请求失败率监控（每分钟）

```sql
-- 监控查询：最近 5 分钟的失败率
SELECT 
    'request_failure_rate_5m' as metric_name,
    ROUND(
        COUNT(CASE WHEN status != 200 THEN 1 END)::numeric / 
        NULLIF(COUNT(*), 0) * 100, 
        2
    ) as value,
    NOW() as timestamp
FROM request_logs
WHERE created_at > NOW() - INTERVAL '5 minutes';

-- 告警阈值：value > 5 (失败率 > 5%)
```

---

## 日志告警规则

### 1. 错误日志频率监控

使用日志聚合系统（如 Loki/ELK）配置告警：

```yaml
# Loki 告警规则
- alert: HighErrorLogRate
  expr: |
    sum(rate({job="llm-gateway"} |= "ERROR" [5m])) > 10
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "错误日志频率过高"
    description: "最近 5 分钟错误日志 {{ $value | humanize }}/s"
```

### 2. 特定错误模式监控

```yaml
# plan_type 列缺失错误
- alert: PlanTypeColumnMissing
  expr: |
    sum(rate({job="llm-gateway"} |~ "column .* does not exist" [5m])) > 0
  for: 1m
  labels:
    severity: critical
    component: database
  annotations:
    summary: "数据库架构错误"
    description: "检测到数据库列缺失错误，可能需要运行迁移"
```

---

## 告警通知配置

### Alertmanager 配置示例

```yaml
route:
  receiver: 'default-receiver'
  group_by: ['severity', 'component']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
  
  routes:
  # 关键告警立即通知
  - match:
      severity: critical
    receiver: 'critical-receiver'
    group_wait: 10s
    repeat_interval: 1h
    
  # 警告级别告警
  - match:
      severity: warning
    receiver: 'warning-receiver'
    repeat_interval: 12h

receivers:
- name: 'default-receiver'
  webhook_configs:
  - url: 'http://alertmanager-webhook:__PORT_8__/alert'

- name: 'critical-receiver'
  webhook_configs:
  - url: 'http://alertmanager-webhook:__PORT_8__/alert'
  email_configs:
  - to: 'ops-critical@example.com'
    from: 'alertmanager@example.com'
    smarthost: 'smtp.example.com:587'
    auth_username: 'alertmanager'
    auth_password: '<password>'
    headers:
      Subject: '【紧急】LLM Gateway 关键告警'

- name: 'warning-receiver'
  webhook_configs:
  - url: 'http://alertmanager-webhook:__PORT_8__/alert'
  slack_configs:
  - api_url: '<slack_webhook_url>'
    channel: '#llm-gateway-alerts'
    title: 'LLM Gateway 告警'
    text: '{{ range .Alerts }}{{ .Annotations.summary }}: {{ .Annotations.description }}{{ end }}'
```

---

## 健康检查脚本

### 定时健康检查（每 5 分钟执行）

```bash
#!/bin/bash
# 文件：__SERVER_PATH_9__

GATEWAY_URL="http://localhost:__PORT_3__"
ALERT_THRESHOLD=3
CONSECUTIVE_FAILURES=0

while true; do
    # 检查健康端点
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" $GATEWAY_URL/healthz)
    
    if [ "$HTTP_CODE" != "200" ]; then
        ((CONSECUTIVE_FAILURES++))
        echo "[$(date)] Health check failed: HTTP $HTTP_CODE (failures: $CONSECUTIVE_FAILURES)"
        
        if [ $CONSECUTIVE_FAILURES -ge $ALERT_THRESHOLD ]; then
            # 发送告警
            curl -X POST http://alertmanager:9093/api/v1/alerts -d '[{
                "labels": {
                    "alertname": "HealthCheckFailed",
                    "severity": "critical",
                    "instance": "'"$(hostname)"'"
                },
                "annotations": {
                    "summary": "LLM Gateway 健康检查失败",
                    "description": "连续 '"$CONSECUTIVE_FAILURES"' 次健康检查失败"
                }
            }]'
        fi
    else
        if [ $CONSECUTIVE_FAILURES -gt 0 ]; then
            echo "[$(date)] Health check recovered after $CONSECUTIVE_FAILURES failures"
        fi
        CONSECUTIVE_FAILURES=0
    fi
    
    sleep 300  # 每 5 分钟检查一次
done
```

---

## 仪表盘配置（Grafana）

### 关键指标面板

```json
{
  "dashboard": {
    "title": "LLM Gateway 运维监控",
    "panels": [
      {
        "title": "请求成功率（5m）",
        "targets": [{
          "expr": "rate(llm_gateway_requests_total{status=\"200\"}[5m]) / rate(llm_gateway_requests_total[5m])"
        }],
        "type": "graph",
        "thresholds": [
          {"value": 0.95, "color": "yellow"},
          {"value": 0.99, "color": "green"}
        ]
      },
      {
        "title": "可用模型绑定数",
        "targets": [{
          "expr": "llm_gateway_available_model_bindings"
        }],
        "type": "stat",
        "thresholds": [
          {"value": 100, "color": "red"},
          {"value": 500, "color": "yellow"},
          {"value": 1000, "color": "green"}
        ]
      },
      {
        "title": "P95 响应延迟",
        "targets": [{
          "expr": "histogram_quantile(0.95, rate(llm_gateway_request_duration_seconds_bucket[5m]))"
        }],
        "type": "graph",
        "unit": "s"
      },
      {
        "title": "错误类型分布",
        "targets": [{
          "expr": "sum by (error_code) (rate(llm_gateway_requests_total{status!=\"200\"}[5m]))"
        }],
        "type": "piechart"
      }
    ]
  }
}
```

---

## 快速检查命令

### 每日检查清单

```bash
# 1. 检查模型发现状态
docker logs llm-gateway-go | grep "model discovery completed" | tail -1

# 2. 检查计费模式一致性
PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway -c "
SELECT COUNT(*) as mismatch FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token');"

# 3. 检查最近 1 小时的成功率
PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway -c "
SELECT 
    COUNT(CASE WHEN status = 200 THEN 1 END) * 100.0 / COUNT(*) as success_rate
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';"

# 4. 检查不健康的凭据
PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway -c "
SELECT id, label, circuit_state, availability_state, quota_state
FROM credentials
WHERE status = 'active'
  AND (circuit_state = 'open' OR availability_state = 'unavailable');"
```

---

## 实施步骤

1. **部署 Prometheus 告警规则**
   ```bash
   kubectl apply -f prometheus-rules.yaml
   ```

2. **配置 Alertmanager**
   ```bash
   kubectl apply -f alertmanager-config.yaml
   ```

3. **部署健康检查脚本**
   ```bash
   scp health-check.sh __SSH_TARGET_2__:/opt/llm-gateway/scripts/
   ssh __SSH_TARGET_2__ "chmod +x __SERVER_PATH_9__"
   ssh __SSH_TARGET_2__ "nohup __SERVER_PATH_9__ > __SERVER_PATH_10__ 2>&1 &"
   ```

4. **导入 Grafana 仪表盘**
   ```bash
   curl -X POST http://grafana:3000/api/dashboards/db \
     -H "Content-Type: application/json" \
     -d @grafana-dashboard.json
   ```

---

**创建日期：** 2026-07-03  
**维护者：** AI 运维团队  
**更新频率：** 每季度review
