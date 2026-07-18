# 08 — MaaS 号池调度：实施 Runbook

> 日期：2026-07-18
> 版本：v2 Production
> 适用环境：kaixuan-1 / 154 / 245 / 252

---

## 使用说明

本 Runbook 是**生产操作手册**，涵盖：
- 日常运维命令
- 故障诊断流程
- 应急响应预案
- 配置变更 SOP
- 监控告警处理

---

## 第一部分：日常运维

### 1.1 查看当前调度策略

```bash
# 查询当前生效的 routing pipeline
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT
    provider_id,
    routing_pipeline->>'name' as strategy,
    jsonb_array_length(routing_pipeline->'stages') as stage_count,
    routing_pipeline
FROM provider_policies
ORDER BY provider_id;
"
```

**预期输出**：
```
 provider_id | strategy | stage_count |           routing_pipeline
-------------+----------+-------------+---------------------------------------
           1 | bandit   |           4 | {"name":"bandit","stages":[...]}
           2 | hybrid   |           4 | {"name":"hybrid","stages":[...]}
```

---

### 1.2 查看凭据权重分布

```bash
# 从 Redis 获取实时权重（WRR 运行时状态）
redis-cli -h $REDIS_HOST -p $REDIS_PORT GET "scheduler:wrr:weights:provider:1"
```

**预期输出**（JSON）：
```json
{
  "credentials": [
    {"credential_id": 17, "weight": 60, "current_weight": -40},
    {"credential_id": 42, "weight": 90, "current_weight": 50},
    {"credential_id": 88, "weight": 30, "current_weight": -20}
  ],
  "total_weight": 180,
  "cycle_count": 12345,
  "last_updated": "2026-07-18T18:54:00Z"
}
```

---

### 1.3 查看号池水位

```bash
# 从 Prometheus 查询当前水位
curl -s "http://localhost:9090/api/v1/query?query=llmgw_watermark_usage_rate" | jq '.data.result'

# 或直接查日志
tail -f /var/log/llm-gateway-go/app.log | grep watermark
```

**预期输出**：
```json
[{
  "metric": {"provider_id": "1"},
  "value": [1721308440, "0.673"]  // 使用率 67.3%
}]
```

---

### 1.4 手动触发水位检查

```bash
# 通过内部 HTTP API 触发（需要在代码中实现）
curl -X POST http://localhost:8080/internal/watermark/check
```

---

### 1.5 查看排队队列状态

```bash
# Redis 查看队列长度
redis-cli -h $REDIS_HOST LLEN "limiter:queue:credential:17"

# 查看队列中的请求
redis-cli -h $REDIS_HOST LRANGE "limiter:queue:credential:17" 0 10
```

---

## 第二部分：配置变更 SOP

### 2.1 切换调度策略（Bandit → WRR）

**场景**：想要给某个 provider 启用 WRR 策略。

**步骤**：

```sql
-- Step 1: 备份当前配置
CREATE TABLE provider_policies_backup_20260718 AS
SELECT * FROM provider_policies WHERE provider_id = 1;

-- Step 2: 更新 routing_pipeline
UPDATE provider_policies
SET routing_pipeline = '{
  "name": "wrr",
  "stages": [
    {"type": "health_filter"},
    {"type": "circuit_breaker_filter"},
    {"type": "rate_limit_filter"},
    {"type": "wrr_selector"}
  ],
  "fallbacks": [
    {"name": "bandit"},
    {"name": "uniform_random"}
  ]
}'::jsonb
WHERE provider_id = 1;

-- Step 3: 验证配置
SELECT provider_id, routing_pipeline
FROM provider_policies
WHERE provider_id = 1;
```

**验证**：

```bash
# 等待 5s（配置热更新周期）
sleep 5

# 查看日志确认策略已切换
tail -100 /var/log/llm-gateway-go/app.log | grep "strategy switched"
```

**预期日志**：
```
2026-07-18T18:55:00Z INFO strategy switched from=bandit to=wrr provider_id=1
```

**Rollback**（如果出问题）：

```sql
-- 恢复备份
UPDATE provider_policies
SET routing_pipeline = (
    SELECT routing_pipeline
    FROM provider_policies_backup_20260718
    WHERE provider_id = 1
)
WHERE provider_id = 1;
```

---

### 2.2 调整权重因子

**场景**：RPM 权重占比从 0.6 调整到 0.7。

```sql
-- 修改权重计算因子（假设我们把它存在 policy 中）
UPDATE provider_policies
SET routing_pipeline = jsonb_set(
    routing_pipeline,
    '{weight_factors}',
    '{"rpm": 0.7, "tpm": 0.2, "success_rate": 0.1}'::jsonb
)
WHERE provider_id = 1;
```

**验证**：

```bash
# 观察权重重新计算
redis-cli GET "scheduler:wrr:weights:provider:1" | jq '.credentials[] | {credential_id, weight}'
```

---

### 2.3 修改水位告警阈值

**场景**：生产环境水位告警过于频繁，提高阈值。

```sql
UPDATE provider_policies
SET
    watermark_alert_warn = 0.80,      -- 从 0.70 提高到 0.80
    watermark_alert_critical = 0.90   -- 从 0.85 提高到 0.90
WHERE provider_id = 1;
```

**验证**：

```bash
# 观察告警是否减少
curl -s http://localhost:9090/api/v1/query?query=rate(llmgw_watermark_alerts_total[5m])
```

---

### 2.4 启用/禁用排队缓冲

**场景**：高并发场景启用排队缓冲。

```bash
# 方式 1: 环境变量（需要重启）
export QUEUE_ENABLED=true
export QUEUE_MAX_SIZE=1000
export QUEUE_TIMEOUT_MS=5000
systemctl restart llm-gateway-go

# 方式 2: 动态配置（推荐，如果实现了热更新）
curl -X POST http://localhost:8080/internal/config/queue \
  -H "Content-Type: application/json" \
  -d '{"enabled": true, "max_size": 1000, "timeout_ms": 5000}'
```

---

## 第三部分：故障诊断

### 3.1 症状：选路返回 nil（无可用凭据）

**诊断流程**：

```bash
# Step 1: 检查候选数量
psql -c "
SELECT
    availability_state,
    COUNT(*)
FROM credentials
WHERE provider_id = 1
GROUP BY availability_state;
"
```

**预期**：至少有 1 个 `available` 状态的凭据。

**如果全部 degraded/quarantined**：

```bash
# Step 2: 查看熔断器状态
redis-cli HGETALL "breaker:credential:17"
```

**输出示例**：
```
state: open
consecutive_failures: 3
last_failure_at: 1721308000
recovery_time: 1721308900
```

**修复**：

```bash
# 手动恢复凭据（谨慎使用）
redis-cli HSET "breaker:credential:17" state closed
redis-cli HDEL "breaker:credential:17" consecutive_failures

# 或等待自动恢复（冷却期过后）
```

---

### 3.2 症状：WRR 权重全为 0

**诊断**：

```bash
redis-cli GET "scheduler:wrr:weights:provider:1" | jq '.credentials[] | select(.weight == 0)'
```

**根因**：所有凭据遭遇 429，权重被自动衰减到 0。

**修复**：

```bash
# 方式 1: 等待自动恢复（冷却期过后权重逐步恢复）

# 方式 2: 手动重置权重（紧急情况）
curl -X POST http://localhost:8080/internal/scheduler/reset-weights \
  -H "Content-Type: application/json" \
  -d '{"provider_id": 1, "reason": "emergency_reset"}'
```

---

### 3.3 症状：水位告警误报

**诊断**：

```bash
# 查看实际使用率
psql -c "
SELECT
    COUNT(*) FILTER (WHERE availability_state = 'available') as available_count,
    SUM(rpm_limit) FILTER (WHERE availability_state = 'available') as total_rpm
FROM credentials
WHERE provider_id = 1;
"

# 查看实时 RPM 消耗
redis-cli GET "limiter:rpm:global"
```

**如果实际使用率 < 70% 但告警了**：

**根因**：水位计算公式错误，或安全线设置过高。

**修复**：

```sql
-- 调整安全余量
UPDATE provider_policies
SET watermark_safety_margin = 2.0  -- 从 1.5 提高到 2.0（使用率 <50% 才告警）
WHERE provider_id = 1;
```

---

### 3.4 症状：优先级队列饿死（低优请求超时）

**诊断**：

```bash
# 查看队列长度
redis-cli LLEN "limiter:queue:credential:17"

# 查看队列头部请求的等待时间
redis-cli LINDEX "limiter:queue:credential:17" 0 | jq '.enqueue_at'
```

**如果队列中有请求等待 > 60s**：

**根因**：WFQ 权重配置错误，或高优请求过多。

**修复**：

```bash
# 临时禁用排队（让请求快速失败）
curl -X POST http://localhost:8080/internal/config/queue \
  -d '{"enabled": false}'

# 或调整 WFQ 权重（提高等待时间的权重）
# 在代码中修改: priority*0.5 + normalizedWait*0.5（从 0.7/0.3 调整）
```

---

### 3.5 症状：策略切换后性能下降

**诊断**：

```bash
# 对比切换前后的 P99 延迟
curl -s "http://localhost:9090/api/v1/query?query=histogram_quantile(0.99, rate(llmgw_request_duration_seconds_bucket[5m]))"

# 查看选路耗时
curl -s "http://localhost:9090/api/v1/query?query=histogram_quantile(0.99, rate(llmgw_scheduler_select_duration_seconds_bucket[5m]))"
```

**如果选路 P99 > 1ms**：

**根因**：候选数量过多（N > 100），O(N) WRR 选路成为瓶颈。

**修复**：

```bash
# 方式 1: 切回 Bandit（性能更好）
# 方式 2: 启用 WRR 分片模式（需要代码实现）
# 方式 3: 增加候选过滤（减少 N）
```

---

## 第四部分：应急响应

### 4.1 紧急情况：所有凭据耗尽

**症状**：
- 所有请求返回 503
- 日志大量 "no available credentials"

**一键恢复**：

```bash
#!/bin/bash
# emergency-restore-credentials.sh

# 1. 重置所有熔断器
redis-cli --scan --pattern "breaker:credential:*" | xargs -L 1 redis-cli DEL

# 2. 重置所有权重
curl -X POST http://localhost:8080/internal/scheduler/reset-all-weights

# 3. 清空排队队列（避免积压）
redis-cli --scan --pattern "limiter:queue:*" | xargs -L 1 redis-cli DEL

# 4. 通知团队
curl -X POST $FEISHU_WEBHOOK \
  -H "Content-Type: application/json" \
  -d '{
    "msg_type": "text",
    "content": {
      "text": "🚨 P0: 凭据池紧急恢复已执行，请立即检查上游状态"
    }
  }'

echo "Emergency restore completed at $(date)"
```

**执行**：

```bash
bash /opt/llm-gateway-go/scripts/emergency-restore-credentials.sh
```

---

### 4.2 紧急情况：水位监控拖垮 DB

**症状**：
- DB CPU 飙升到 100%
- 慢查询日志大量 `SELECT COUNT(*) FROM credentials`

**一键止血**：

```bash
# 临时禁用水位监控
export WATERMARK_MONITOR_ENABLED=false
pkill -SIGHUP llm-gateway-go  # 重新加载配置（不重启）

# 或直接杀掉后台 goroutine（需要实现信号处理）
curl -X POST http://localhost:8080/internal/watermark/disable
```

**根本修复**（事后）：

```sql
-- 创建物化视图
CREATE MATERIALIZED VIEW credential_pool_stats AS
SELECT
    availability_state,
    COUNT(*) as cnt,
    SUM(COALESCE(rpm_limit, 0)) as total_rpm
FROM credentials
GROUP BY availability_state;

CREATE UNIQUE INDEX ON credential_pool_stats (availability_state);

-- 修改监控查询目标
-- 从 credentials 表改为 credential_pool_stats 视图
```

---

### 4.3 紧急情况：告警风暴

**症状**：
- 5 分钟内收到 100+ 条飞书告警
- 告警内容："号池水位偏高"

**止血**：

```bash
# 方式 1: 临时提高告警阈值
psql -c "
UPDATE provider_policies
SET watermark_alert_warn = 0.95,
    watermark_alert_critical = 0.99;
"

# 方式 2: 临时禁用告警
curl -X POST http://localhost:8080/internal/alerts/mute \
  -d '{"pattern": "watermark.*", "duration": "1h"}'
```

---

### 4.4 紧急情况：Rollback 到 Bandit

**场景**：WRR/Hybrid 策略出现严重问题，需要秒切回 Bandit。

**一键 Rollback**：

```bash
#!/bin/bash
# rollback-to-bandit.sh

PROVIDER_ID=${1:-1}

# 1. 备份当前配置
psql -c "
CREATE TABLE IF NOT EXISTS provider_policies_rollback_$(date +%Y%m%d_%H%M%S) AS
SELECT * FROM provider_policies WHERE provider_id = $PROVIDER_ID;
"

# 2. 切换到 Bandit
psql -c "
UPDATE provider_policies
SET routing_pipeline = '{
  \"name\": \"bandit\",
  \"stages\": [
    {\"type\": \"health_filter\"},
    {\"type\": \"circuit_breaker_filter\"},
    {\"type\": \"rate_limit_filter\"},
    {\"type\": \"bandit_selector\"}
  ],
  \"fallbacks\": [
    {\"name\": \"uniform_random\"}
  ]
}'::jsonb
WHERE provider_id = $PROVIDER_ID;
"

# 3. 验证
sleep 5
curl -s http://localhost:8080/internal/scheduler/status | jq ".providers[] | select(.provider_id == $PROVIDER_ID)"

# 4. 通知
curl -X POST $FEISHU_WEBHOOK \
  -H "Content-Type: application/json" \
  -d "{
    \"msg_type\": \"text\",
    \"content\": {
      \"text\": \"⚠️ Provider $PROVIDER_ID 已 Rollback 到 Bandit 策略\"
    }
  }"

echo "Rollback completed for provider $PROVIDER_ID"
```

**执行**：

```bash
bash /opt/llm-gateway-go/scripts/rollback-to-bandit.sh 1
```

---

## 第五部分：监控与告警

### 5.1 Prometheus 告警规则

```yaml
# /etc/prometheus/rules/llm-gateway-scheduler.yml

groups:
  - name: scheduler
    interval: 30s
    rules:
      # 水位告警
      - alert: PoolWatermarkHigh
        expr: llmgw_watermark_usage_rate > 0.70
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "号池水位偏高 ({{ $value | humanizePercentage }})"
          description: "Provider {{ $labels.provider_id }} 使用率 {{ $value | humanizePercentage }}"

      - alert: PoolWatermarkCritical
        expr: llmgw_watermark_usage_rate > 0.85
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "号池水位告急 ({{ $value | humanizePercentage }})"
          description: "Provider {{ $labels.provider_id }} 使用率 {{ $value | humanizePercentage }}, 需要立即补充凭据"

      # 选路延迟告警
      - alert: SchedulerSelectSlow
        expr: histogram_quantile(0.99, rate(llmgw_scheduler_select_duration_seconds_bucket[5m])) > 0.001
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "调度器选路 P99 延迟 > 1ms"
          description: "当前 P99 = {{ $value | humanizeDuration }}"

      # 降级告警
      - alert: SchedulerFallbackTriggered
        expr: rate(llmgw_scheduler_fallback_total[5m]) > 0.01
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "调度器触发降级"
          description: "Provider {{ $labels.provider_id }} 主策略失败，触发降级链"
```

---

### 5.2 Grafana Dashboard

**面板配置**（JSON 模板）：

```json
{
  "dashboard": {
    "title": "LLM Gateway - 号池调度",
    "panels": [
      {
        "title": "号池水位（实时）",
        "targets": [{
          "expr": "llmgw_watermark_usage_rate",
          "legendFormat": "Provider {{provider_id}}"
        }],
        "type": "graph",
        "yaxes": [{"format": "percentunit", "max": 1, "min": 0}]
      },
      {
        "title": "凭据状态分布",
        "targets": [{
          "expr": "llmgw_watermark_healthy_creds",
          "legendFormat": "Healthy"
        }, {
          "expr": "llmgw_watermark_degraded_creds",
          "legendFormat": "Degraded"
        }, {
          "expr": "llmgw_watermark_circuit_open",
          "legendFormat": "Circuit Open"
        }],
        "type": "graph"
      },
      {
        "title": "WRR 权重分布",
        "targets": [{
          "expr": "llmgw_scheduler_wrr_weight",
          "legendFormat": "Cred {{credential_id}}"
        }],
        "type": "graph"
      },
      {
        "title": "调度策略分布",
        "targets": [{
          "expr": "llmgw_scheduler_strategy",
          "legendFormat": "{{strategy}}"
        }],
        "type": "stat"
      },
      {
        "title": "选路延迟 P50/P99",
        "targets": [{
          "expr": "histogram_quantile(0.50, rate(llmgw_scheduler_select_duration_seconds_bucket[5m]))",
          "legendFormat": "P50"
        }, {
          "expr": "histogram_quantile(0.99, rate(llmgw_scheduler_select_duration_seconds_bucket[5m]))",
          "legendFormat": "P99"
        }],
        "type": "graph",
        "yaxes": [{"format": "s"}]
      }
    ]
  }
}
```

**导入**：

```bash
# 通过 Grafana API 导入
curl -X POST http://localhost:3000/api/dashboards/db \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $GRAFANA_API_KEY" \
  -d @llm-gateway-scheduler-dashboard.json
```

---

### 5.3 飞书告警 Webhook

**配置**：

```bash
# 在 provider_policies 中配置 Webhook URL
psql -c "
ALTER TABLE provider_policies ADD COLUMN IF NOT EXISTS alert_webhook TEXT;

UPDATE provider_policies
SET alert_webhook = 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx'
WHERE provider_id = 1;
"
```

**测试告警**：

```bash
curl -X POST https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx \
  -H "Content-Type: application/json" \
  -d '{
    "msg_type": "interactive",
    "card": {
      "header": {
        "title": {
          "content": "🚨 号池水位告警",
          "tag": "plain_text"
        },
        "template": "red"
      },
      "elements": [{
        "tag": "div",
        "text": {
          "content": "**Provider**: 1\n**使用率**: 87%\n**可用 RPM**: 1200 / 1500\n**时间**: 2026-07-18 18:54:00",
          "tag": "lark_md"
        }
      }, {
        "tag": "action",
        "actions": [{
          "tag": "button",
          "text": {
            "content": "查看 Grafana",
            "tag": "plain_text"
          },
          "url": "http://grafana.example.com/d/scheduler",
          "type": "default"
        }, {
          "tag": "button",
          "text": {
            "content": "紧急恢复",
            "tag": "plain_text"
          },
          "url": "http://ops.example.com/runbook/scheduler#emergency",
          "type": "danger"
        }]
      }]
    }
  }'
```

---

## 第六部分：最佳实践

### 6.1 每日例行检查

```bash
#!/bin/bash
# daily-health-check.sh

echo "=== 号池调度健康检查 $(date) ==="

# 1. 水位检查
echo -e "\n[1] 号池水位:"
psql -t -c "
SELECT
    provider_id,
    ROUND((SELECT metric_value FROM metrics WHERE metric_name = 'watermark_usage_rate' AND provider_id = p.provider_id), 2) as usage_rate
FROM provider_policies p;
" | column -t

# 2. 凭据状态分布
echo -e "\n[2] 凭据状态:"
psql -t -c "
SELECT
    availability_state,
    COUNT(*)
FROM credentials
GROUP BY availability_state;
" | column -t

# 3. 选路性能
echo -e "\n[3] 选路 P99 延迟:"
curl -s "http://localhost:9090/api/v1/query?query=histogram_quantile(0.99, rate(llmgw_scheduler_select_duration_seconds_bucket[1h]))" \
  | jq -r '.data.result[] | "\(.metric.provider_id): \(.value[1])s"'

# 4. 降级触发次数
echo -e "\n[4] 降级触发次数 (24h):"
curl -s "http://localhost:9090/api/v1/query?query=increase(llmgw_scheduler_fallback_total[24h])" \
  | jq -r '.data.result[] | "\(.metric.provider_id): \(.value[1])"'

echo -e "\n=== 检查完成 ==="
```

**设置 cron**：

```bash
# 每天 10:00 执行健康检查
0 10 * * * /opt/llm-gateway-go/scripts/daily-health-check.sh > /var/log/scheduler-health-check.log 2>&1
```

---

### 6.2 配置变更前的 Dry-run

```bash
# 在 staging 环境测试配置变更
psql -h staging-db -c "
UPDATE provider_policies
SET routing_pipeline = '<新配置>'::jsonb
WHERE provider_id = 1;
"

# 观察 30 分钟
sleep 1800

# 检查错误率/延迟是否正常
curl -s "http://staging:9090/api/v1/query?query=rate(llmgw_request_errors_total[30m])"
```

---

### 6.3 灰度发布 Checklist

```
□ 1. 在 kaixuan-1 测试环境验证 48h
  - 无异常日志
  - 选路分布符合预期
  - 性能无退化

□ 2. 在 154 启用单个低流量模型
  - provider_id = <test_provider>
  - 观察 24h

□ 3. 逐步放量
  - 5% 流量（1d）
  - 20% 流量（1d）
  - 50% 流量（1d）
  - 100% 流量

□ 4. 每个阶段检查
  - 水位是否健康
  - 告警是否误报
  - 降级是否过于频繁
  - 用户请求成功率无下降

□ 5. 准备 Rollback 预案
  - 一键切回 Bandit 脚本就绪
  - 值班人员知晓操作步骤
```

---

## 附录：快速参考

### 常用命令速查

```bash
# 查看当前策略
psql -c "SELECT provider_id, routing_pipeline->>'name' FROM provider_policies;"

# 查看水位
curl -s localhost:9090/api/v1/query?query=llmgw_watermark_usage_rate | jq

# 重置权重
curl -X POST localhost:8080/internal/scheduler/reset-weights -d '{"provider_id":1}'

# 一键 Rollback
bash /opt/llm-gateway-go/scripts/rollback-to-bandit.sh 1

# 紧急恢复凭据
bash /opt/llm-gateway-go/scripts/emergency-restore-credentials.sh
```

---

**Runbook 版本**: v1.0
**最后更新**: 2026-07-18
**维护**: SRE Team
**紧急联系**: oncall@example.com
