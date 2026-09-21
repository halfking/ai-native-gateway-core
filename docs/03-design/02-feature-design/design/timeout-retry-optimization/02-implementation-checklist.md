# 实施检查清单与监控方案

## ✅ 实施前检查清单

### 1. 环境准备
- [ ] 154服务器SSH访问正常
- [ ] 数据库访问正常（PG连接）
- [ ] 备份当前配置文件
- [ ] 通知相关团队成员

### 2. 依赖确认
- [ ] Go版本 >= 1.19
- [ ] PostgreSQL版本 >= 14
- [ ] Redis连接正常
- [ ] 磁盘空间充足（至少5GB剩余）

### 3. 回滚准备
- [ ] 备份脚本准备好
- [ ] 数据库备份完成
- [ ] 回滚步骤文档化
- [ ] 监控告警配置就绪

---

## 📋 分阶段实施清单

### Phase 0: 立即优化（今天，20分钟）

**目标**: 快速降低超时率

- [ ] **Step 1**: SSH到154服务器
  ```bash
  ssh root@<env:HOST_154_IP> -p 25022
  ```

- [ ] **Step 2**: 备份现有配置
  ```bash
  cd /etc/llm-gateway-go
  cp env env.bak.$(date +%Y%m%d-%H%M%S)
  ```

- [ ] **Step 3**: 查看当前配置
  ```bash
  cat env | grep -E "TIMEOUT|KEEPALIVE|RETRY"
  ```

- [ ] **Step 4**: 修改配置
  ```bash
  # 方案A: 使用sed（推荐）
  sed -i 's/LLM_GATEWAY_UPSTREAM_TIMEOUT=30/LLM_GATEWAY_UPSTREAM_TIMEOUT=90/' env
  
  # 方案B: 手动编辑
  vi env
  # 找到 LLM_GATEWAY_UPSTREAM_TIMEOUT=30
  # 改为 LLM_GATEWAY_UPSTREAM_TIMEOUT=90
  ```

- [ ] **Step 5**: 添加新配置
  ```bash
  # 如果不存在则添加
  grep -q "LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE" env || \
    echo "LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=true" >> env
  
  grep -q "LLM_GATEWAY_KEEPALIVE_INTERVAL" env || \
    echo "LLM_GATEWAY_KEEPALIVE_INTERVAL=15" >> env
  
  grep -q "LLM_GATEWAY_STREAM_RETRY_THRESHOLD" env || \
    echo "LLM_GATEWAY_STREAM_RETRY_THRESHOLD=3" >> env
  ```

- [ ] **Step 6**: 验证配置
  ```bash
  cat env | grep -E "TIMEOUT|KEEPALIVE|RETRY"
  # 应该看到:
  # LLM_GATEWAY_UPSTREAM_TIMEOUT=90
  # LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=true
  # LLM_GATEWAY_KEEPALIVE_INTERVAL=15
  # LLM_GATEWAY_STREAM_RETRY_THRESHOLD=3
  ```

- [ ] **Step 7**: 重启服务
  ```bash
  systemctl restart llm-gateway-go
  ```

- [ ] **Step 8**: 验证服务启动
  ```bash
  # 检查服务状态
  systemctl status llm-gateway-go
  
  # 查看最新日志
  journalctl -u llm-gateway-go -n 50 --no-pager
  
  # 监控实时日志
  journalctl -u llm-gateway-go -f
  ```

- [ ] **Step 9**: 健康检查
  ```bash
  # 从154本地检查
  curl -s http://localhost:8080/healthz | jq
  
  # 从252检查154
  curl -s http://<env:HOST_252_INTERNAL_IP>:8080/healthz | jq
  ```

- [ ] **Step 10**: 监控30分钟，观察效果

---

### Phase 1: 数据库Schema扩展（第2天，4小时）

**目标**: 添加配置表和扩展字段

- [ ] **Step 1**: 连接到PostgreSQL
  ```bash
  # 从252连接
  psql -h <env:HOST_252_INTERNAL_IP> -U postgres -d llm_gateway
  ```

- [ ] **Step 2**: 创建 system_settings 表
  ```sql
  -- 执行SQL
  \i /path/to/migrations/001_create_system_settings.sql
  ```

- [ ] **Step 3**: 插入初始配置
  ```sql
  \i /path/to/migrations/002_insert_default_settings.sql
  ```

- [ ] **Step 4**: 扩展 request_logs 表
  ```sql
  \i /path/to/migrations/003_extend_request_logs.sql
  ```

- [ ] **Step 5**: 创建 session_last_requests 表
  ```sql
  \i /path/to/migrations/004_create_session_last_requests.sql
  ```

- [ ] **Step 6**: 验证表结构
  ```sql
  \d system_settings
  \d+ request_logs
  \d session_last_requests
  ```

- [ ] **Step 7**: 创建索引
  ```sql
  CREATE INDEX CONCURRENTLY idx_request_logs_continuation 
    ON request_logs(session_id, created_at DESC) 
    WHERE is_continuation = true;
  ```

---

### Phase 2: 动态超时实现（第3-4天，2天）

**目标**: 根据上下文动态调整超时

- [ ] **Step 1**: 创建 timeout_config.go
  ```bash
  cd /path/to/llm-gateway-go-3
  touch config/timeout_config.go
  ```

- [ ] **Step 2**: 实现 TimeoutConfig 结构
  - [ ] 基础结构定义
  - [ ] CalculateEffectiveTimeout 方法
  - [ ] ReloadFromDB 热加载方法
  - [ ] 单元测试

- [ ] **Step 3**: 集成到 Executor
  - [ ] 修改 domains/streaming/executors/executor.go
  - [ ] 在请求前调用动态超时计算
  - [ ] 记录 effective_timeout 到日志

- [ ] **Step 4**: 添加配置热加载定时器
  ```go
  // 在 main.go 启动时
  go func() {
      ticker := time.NewTicker(30 * time.Second)
      for range ticker.C {
          globalTimeoutConfig.ReloadFromDB(ctx, db)
      }
  }()
  ```

- [ ] **Step 5**: 本地测试
  ```bash
  go test ./config -v -run TestTimeoutConfig
  go test ./domains/streaming -v -run TestDynamicTimeout
  ```

- [ ] **Step 6**: 部署到154测试

---

### Phase 3: Keepalive & 节点切换（第5-6天，2天）

**目标**: 实时通知客户端状态

- [ ] **Step 1**: 创建 keepalive_sender.go
  ```bash
  touch domains/streaming/keepalive_sender.go
  ```

- [ ] **Step 2**: 实现 KeepaliveSender
  - [ ] Start/Stop 方法
  - [ ] SendKeepalive 方法
  - [ ] SendNodeSwitch 方法
  - [ ] SendRetryWait 方法

- [ ] **Step 3**: 集成到流式处理
  - [ ] 在 executor 启动时创建 KeepaliveSender
  - [ ] 节点切换时发送通知
  - [ ] 最后节点重试时发送通知

- [ ] **Step 4**: SSE格式测试
  ```bash
  # 测试客户端接收
  curl -N http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{"model":"minimax-m3","messages":[...],"stream":true}'
  ```

- [ ] **Step 5**: 前端SDK适配（可选）

---

### Phase 4: 继续/重试检测（第7-9天，3天）

**目标**: 智能复用已有响应

- [ ] **Step 1**: 创建 continuation_detector.go
  ```bash
  touch domains/streaming/continuation_detector.go
  ```

- [ ] **Step 2**: 实现关键功能
  - [ ] IsContinuation 方法
  - [ ] GetLastRequestStatus 方法
  - [ ] SaveResponseCache 方法
  - [ ] ReloadKeywords 热加载

- [ ] **Step 3**: 集成到请求处理流程
  - [ ] 请求开始时检测继续语义
  - [ ] 查询上次请求状态
  - [ ] 决策：复用缓存 vs 新请求

- [ ] **Step 4**: 缓存管理
  - [ ] 实现缓存写入
  - [ ] 实现缓存清理cron job
  - [ ] 监控缓存命中率

- [ ] **Step 5**: 端到端测试
  ```bash
  # 测试场景1: 客户端断开 + 继续请求
  # 测试场景2: 超时 + 重试请求
  # 测试场景3: 成功 + 继续请求（应走正常流程）
  ```

---

## 📊 监控指标定义

### 1. 核心业务指标

| 指标名称 | 计算方式 | 目标值 | 告警阈值 |
|---------|---------|--------|---------|
| 总体超时率 | timeout_requests / total_requests | <3% | >5% |
| Minimax超时率 | minimax_timeout / minimax_total | <5% | >8% |
| 平均响应时间 | AVG(latency_ms) | <12s | >15s |
| P95响应时间 | PERCENTILE_95(latency_ms) | <30s | >45s |
| P99响应时间 | PERCENTILE_99(latency_ms) | <60s | >90s |

### 2. 重试与切换指标

| 指标名称 | 计算方式 | 目标值 | 告警阈值 |
|---------|---------|--------|---------|
| 节点切换率 | node_switch_count / total_requests | <5% | >10% |
| 多次切换率 | switch_count>1 / total_requests | <1% | >3% |
| 重试成功率 | retry_success / retry_total | >90% | <80% |
| 熔断触发率 | circuit_open_events / total_requests | <2% | >5% |

### 3. Token优化指标

| 指标名称 | 计算方式 | 目标值 |
|---------|---------|--------|
| 缓存命中率 | cached_responses / continuation_requests | >60% |
| Token节省量 | saved_tokens / (saved_tokens + consumed_tokens) | >8% |
| 平均Token节省 | AVG(saved_tokens_per_cached_request) | >5000 |

---

## 🔍 监控SQL查询集合

### 查询1: 实时超时监控（每5分钟）

```sql
-- 保存为 monitoring/queries/timeout_rate_5min.sql
SELECT 
    date_trunc('minute', created_at) as time_bucket,
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') as timeout_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') / COUNT(*), 2) as timeout_rate
FROM request_logs
WHERE created_at > NOW() - INTERVAL '5 minutes'
GROUP BY time_bucket
ORDER BY time_bucket DESC
LIMIT 5;
```

### 查询2: 模型级别性能（每小时）

```sql
-- 保存为 monitoring/queries/model_performance_1h.sql
SELECT 
    client_model,
    provider_id,
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE success = true) as success_count,
    COUNT(*) FILTER (WHERE success = false) as failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) as success_rate,
    ROUND(AVG(latency_ms)/1000.0, 2) as avg_latency_sec,
    ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)/1000.0, 2) as p95_latency_sec,
    ROUND(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY latency_ms)/1000.0, 2) as p99_latency_sec
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY client_model, provider_id
HAVING COUNT(*) > 10
ORDER BY total_requests DESC
LIMIT 20;
```

### 查询3: 缓存效果统计（每小时）

```sql
-- 保存为 monitoring/queries/cache_effectiveness_1h.sql
SELECT 
    COUNT(*) FILTER (WHERE is_continuation = true) as continuation_requests,
    COUNT(*) FILTER (WHERE cached_response_id IS NOT NULL) as cache_hits,
    COUNT(*) FILTER (WHERE is_continuation = true AND cached_response_id IS NULL) as cache_misses,
    ROUND(100.0 * COUNT(*) FILTER (WHERE cached_response_id IS NOT NULL) / 
        NULLIF(COUNT(*) FILTER (WHERE is_continuation = true), 0), 2) as cache_hit_rate,
    SUM(COALESCE(context_size_tokens, 0)) FILTER (WHERE cached_response_id IS NOT NULL) as tokens_saved
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
```

### 查询4: 节点切换统计（每小时）

```sql
-- 保存为 monitoring/queries/node_switch_1h.sql
SELECT 
    jsonb_array_length(routing_attempts) - 1 as switch_count,
    COUNT(*) as request_count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND routing_attempts IS NOT NULL
GROUP BY switch_count
ORDER BY switch_count;
```

---

## 📈 Grafana Dashboard配置

### Dashboard JSON模板结构

```json
{
  "dashboard": {
    "title": "LLM Gateway - Timeout & Retry Optimization",
    "panels": [
      {
        "title": "实时超时率",
        "type": "graph",
        "targets": [
          {
            "rawSql": "/* 使用 timeout_rate_5min.sql */"
          }
        ]
      },
      {
        "title": "延迟分布",
        "type": "heatmap",
        "targets": [
          {
            "rawSql": "/* 延迟热力图查询 */"
          }
        ]
      },
      {
        "title": "缓存命中率",
        "type": "stat",
        "targets": [
          {
            "rawSql": "/* 使用 cache_effectiveness_1h.sql */"
          }
        ]
      },
      {
        "title": "节点切换统计",
        "type": "bar",
        "targets": [
          {
            "rawSql": "/* 使用 node_switch_1h.sql */"
          }
        ]
      }
    ]
  }
}
```

---

## 🚨 告警规则配置

### 告警1: 超时率过高

```yaml
# alerts/timeout_rate_high.yml
- alert: TimeoutRateHigh
  expr: |
    (sum(rate(request_logs_timeout_total[5m])) / 
     sum(rate(request_logs_total[5m]))) > 0.05
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "超时率过高: {{ $value | humanizePercentage }}"
    description: "过去10分钟超时率超过5%"
```

### 告警2: 节点切换频繁

```yaml
# alerts/node_switch_frequent.yml
- alert: NodeSwitchFrequent
  expr: |
    sum(rate(node_switch_events_total[5m])) > 10
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "节点切换频繁: {{ $value }} switches/min"
    description: "过去10分钟节点切换过于频繁，可能存在上游问题"
```

### 告警3: 缓存命中率过低

```yaml
# alerts/cache_hit_rate_low.yml
- alert: CacheHitRateLow
  expr: |
    (sum(cache_hits_total) / sum(continuation_requests_total)) < 0.40
  for: 1h
  labels:
    severity: info
  annotations:
    summary: "缓存命中率过低: {{ $value | humanizePercentage }}"
    description: "过去1小时缓存命中率低于40%，检查缓存逻辑"
```

---

## 📝 日志示例

### 成功场景日志

```json
{
  "time": "2026-07-22T23:00:00+08:00",
  "level": "INFO",
  "msg": "request completed with dynamic timeout",
  "request_id": "abc123",
  "model": "minimax-m3",
  "context_tokens": 25000,
  "effective_timeout_seconds": 75,
  "actual_latency_ms": 45000,
  "keepalive_sent_count": 4,
  "success": true
}
```

### 节点切换日志

```json
{
  "time": "2026-07-22T23:05:00+08:00",
  "level": "WARN",
  "msg": "node switch triggered",
  "request_id": "def456",
  "from_provider_id": 18,
  "to_provider_id": 5917,
  "reason": "timeout",
  "attempt": 2,
  "elapsed_seconds": 65
}
```

### 缓存命中日志

```json
{
  "time": "2026-07-22T23:10:00+08:00",
  "level": "INFO",
  "msg": "continuation request served from cache",
  "request_id": "ghi789",
  "session_id": "sess_123",
  "cached_response_id": 12345,
  "continuation_keywords": ["继续"],
  "tokens_saved": 5000
}
```

---

## ✅ 验收标准

### Phase 0 验收（立即优化）

- [ ] 配置已生效（grep验证）
- [ ] 服务重启成功
- [ ] 健康检查通过
- [ ] 30分钟内超时率 <5%

### Phase 1 验收（数据库）

- [ ] 所有表创建成功
- [ ] 索引创建成功
- [ ] 可以插入/查询配置
- [ ] 旧数据不受影响

### Phase 2 验收（动态超时）

- [ ] 单元测试全部通过
- [ ] 日志中可见 effective_timeout
- [ ] 大上下文请求超时时间确实增加
- [ ] 配置热加载生效

### Phase 3 验收（Keepalive）

- [ ] 客户端可接收 keepalive 事件
- [ ] 节点切换通知正常发送
- [ ] 不影响正常数据流
- [ ] 旧客户端兼容

### Phase 4 验收（继续/重试）

- [ ] "请继续"请求可被检测
- [ ] 缓存可正确读写
- [ ] 缓存命中时Token消耗为0
- [ ] 缓存过期自动清理

---

**文档版本**: v1.0  
**创建时间**: 2026-07-22  
**维护者**: Infrastructure Team  
**下次审查**: 2026-08-01
