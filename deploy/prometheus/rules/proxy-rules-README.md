# 代理子系统 Prometheus 告警规则说明

## 概述

本文档说明代理子系统 (`proxy` package) 的 Prometheus 告警规则配置、触发条件和处理建议。

**规则文件**: `proxy-rules.yml`  
**告警组**: `proxy_alerts`  
**评估间隔**: 30 秒

---

## 告警规则清单

### Critical 级别告警 (2 条)

| 告警名称 | 触发条件 | 持续时间 | 影响范围 |
|---------|---------|---------|---------|
| `ProxyNoDialableNodes` | 无可拨号节点 (= 0) | 1 分钟 | 所有代理请求失败，业务完全中断 |
| `ProxyNodeHealthCritical` | 节点健康率 < 50% | 5 分钟 | 超过一半节点不可用，请求失败率显著上升 |

### Warning 级别告警 (4 条)

| 告警名称 | 触发条件 | 持续时间 | 影响范围 |
|---------|---------|---------|---------|
| `ProxySubscriptionRefreshLow` | 订阅刷新成功率 < 95% | 10 分钟 | 节点列表可能过时 |
| `ProxyNodeHealthLow` | 节点健康率 < 80% | 5 分钟 | 可用节点减少，单节点压力增大 |
| `ProxyPasswordDecryptFailed` | 密码解密失败 > 0 | 5 分钟 | 加密节点无法使用 |
| `ProxyNodeSelectionFailureHigh` | 节点选择失败率 > 10% | 5 分钟 | 请求无法获取代理节点 |

### Info 级别告警 (3 条)

| 告警名称 | 触发条件 | 持续时间 | 影响范围 |
|---------|---------|---------|---------|
| `ProxyHealthCheckSlow` | 健康检查耗时 P95 > 5 秒 | 10 分钟 | 节点状态更新延迟 |
| `ProxySubscriptionRefreshSlow` | 订阅刷新耗时 P95 > 30 秒 | 10 分钟 | 订阅更新不及时 |
| `ProxyTransportInvalidationsHigh` | Transport 失效次数 > 50 (10分钟) | 5 分钟 | 连接池频繁重建 |

---

## 告警详细说明

### 1. ProxyNoDialableNodes (Critical)

**触发条件**:
```promql
llm_gateway_proxy_nodes_dialable == 0
```

**含义**: 当前无任何可被 Go 直接拨号的节点（http/https/socks5 协议），所有需要代理的请求将失败。

**可能原因**:
1. 订阅源返回的节点全部是不支持的协议（如 trojan、vless）
2. 订阅刷新失败，节点列表为空
3. 所有节点被标记为不可用
4. 数据库中 `proxy_nodes` 表无数据

**处理步骤**:
```bash
# 1. 检查节点总数
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_total' | jq

# 2. 检查订阅刷新状态
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_subscription_refresh_total' | jq

# 3. 查看最近的订阅刷新日志
journalctl -u llm-gateway-go -n 200 | grep -i "subscription"

# 4. 检查数据库节点数据
psql -U postgres -d llm_gateway -c "SELECT COUNT(*), protocol FROM proxy_nodes GROUP BY protocol;"

# 5. 手动触发订阅刷新（如果有 API）
curl -X POST http://localhost:8080/admin/proxy/refresh-subscriptions
```

**紧急恢复**:
- 切换到备用订阅源
- 手动添加测试节点到数据库
- 临时禁用代理，使用直连模式

---

### 2. ProxyNodeHealthCritical (Critical)

**触发条件**:
```promql
(llm_gateway_proxy_nodes_total - llm_gateway_proxy_nodes_unhealthy) / llm_gateway_proxy_nodes_total < 0.5
```

**含义**: 健康节点占比低于 50%，大部分节点处于不健康状态（连续探活失败 ≥3 次）。

**可能原因**:
1. 代理服务商大面积故障
2. 网络连通性问题（防火墙、路由）
3. 健康检查目标服务（如 Google）不可达
4. 服务器出口带宽耗尽

**处理步骤**:
```bash
# 1. 查看不健康节点数量
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_unhealthy' | jq

# 2. 检查健康检查失败原因分布
curl -s 'http://localhost:9090/api/v1/query?query=rate(llm_gateway_proxy_node_health_check_total[5m])' | jq

# 3. 查看健康检查日志
journalctl -u llm-gateway-go -n 500 | grep -i "health_check"

# 4. 手动测试节点连通性
curl -x socks5://proxy-node-address:port https://www.google.com -v --max-time 5

# 5. 检查服务器网络状态
netstat -s | grep -i "error\|drop"
ifconfig | grep -i error
```

**降级策略**:
- 如果是暂时性问题，等待自动恢复
- 如果是区域性问题，禁用特定区域节点
- 如果是持续性问题，考虑切换订阅源或降级直连

---

### 3. ProxySubscriptionRefreshLow (Warning)

**触发条件**:
```promql
rate(llm_gateway_proxy_subscription_refresh_total{status="success"}[5m]) / rate(llm_gateway_proxy_subscription_refresh_total[5m]) < 0.95
```

**含义**: 订阅刷新成功率低于 95%，可能导致节点列表过时。

**可能原因**:
1. 订阅 URL 不可达（DNS 解析失败、网络超时）
2. 订阅源返回格式错误或空内容
3. 订阅源限流或 HTTP 状态码异常
4. 数据库写入失败

**处理步骤**:
```bash
# 1. 查看失败次数
curl -s 'http://localhost:9090/api/v1/query?query=rate(llm_gateway_proxy_subscription_refresh_total{status!="success"}[5m])' | jq

# 2. 手动测试订阅 URL
SUBSCRIPTION_URL="https://example.com/subscription"
curl -v -L "$SUBSCRIPTION_URL" -o /tmp/subscription.txt
cat /tmp/subscription.txt | head -20

# 3. 检查订阅刷新日志
journalctl -u llm-gateway-go --since "10 minutes ago" | grep "subscription refresh"

# 4. 验证数据库连接
psql -U postgres -d llm_gateway -c "SELECT NOW();"
```

---

### 4. ProxyNodeHealthLow (Warning)

**触发条件**:
```promql
(llm_gateway_proxy_nodes_total - llm_gateway_proxy_nodes_unhealthy) / llm_gateway_proxy_nodes_total < 0.8
```

**含义**: 健康节点占比低于 80%，可用节点数量偏少。

**处理步骤**:
```bash
# 1. 观察趋势
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_unhealthy' | jq

# 2. 检查连续失败次数分布
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_node_consecutive_failures' | jq

# 3. 分析健康检查超时
curl -s 'http://localhost:9090/api/v1/query?query=rate(llm_gateway_proxy_node_health_check_total{status="timeout"}[5m])' | jq
```

---

### 5. ProxyPasswordDecryptFailed (Warning)

**触发条件**:
```promql
increase(llm_gateway_proxy_password_decrypt_failed_total[5m]) > 0
```

**含义**: 有节点的加密密码解密失败，这些节点无法正常初始化。

**可能原因**:
1. 解密密钥 (`PROXY_PASSWORD_KEY`) 未配置或错误
2. 数据库中的密码格式错误（非 `enc:` 前缀或损坏）
3. 密钥轮转后旧密码无法解密
4. 加密算法或格式不兼容

**处理步骤**:
```bash
# 1. 检查环境变量
echo $PROXY_PASSWORD_KEY

# 2. 查看加密密码节点
psql -U postgres -d llm_gateway -c "SELECT server, password FROM proxy_nodes WHERE password LIKE 'enc:%' LIMIT 10;"

# 3. 查看解密失败日志
journalctl -u llm-gateway-go -n 200 | grep -i "password decrypt"

# 4. 测试解密功能（需要内部工具）
./llm-gateway-go-cli decrypt-password "enc:xxxxx"
```

---

### 6. ProxyNodeSelectionFailureHigh (Warning)

**触发条件**:
```promql
rate(llm_gateway_proxy_node_selection_total{result!="success"}[5m]) / rate(llm_gateway_proxy_node_selection_total[5m]) > 0.1
```

**含义**: 节点选择失败率超过 10%，大量请求无法获取可用代理节点。

**可能失败原因**:
- `no_healthy`: 所有节点都不健康
- `no_available`: 根本没有节点
- 其他选择逻辑错误

**处理步骤**:
```bash
# 1. 查看失败原因分布
curl -s 'http://localhost:9090/api/v1/query?query=rate(llm_gateway_proxy_node_selection_total[5m])' | jq

# 2. 检查节点选择耗时
curl -s 'http://localhost:9090/api/v1/query?query=histogram_quantile(0.95, rate(llm_gateway_proxy_node_selection_duration_seconds_bucket[5m]))' | jq

# 3. 查看节点选择日志
journalctl -u llm-gateway-go -n 200 | grep "node selection"
```

---

### 7. ProxyHealthCheckSlow (Info)

**触发条件**:
```promql
histogram_quantile(0.95, rate(llm_gateway_proxy_node_health_check_duration_seconds_bucket[5m])) > 5
```

**含义**: 健康检查耗时 P95 超过 5 秒，可能影响节点状态更新的及时性。

**可能原因**:
1. 健康检查目标服务响应慢
2. 网络延迟高
3. 健康检查并发数过高
4. 健康检查超时时间设置过长

**处理步骤**:
```bash
# 1. 检查响应时间分布
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_node_response_time_ms' | jq

# 2. 查看超时次数
curl -s 'http://localhost:9090/api/v1/query?query=rate(llm_gateway_proxy_node_health_check_total{status="timeout"}[5m])' | jq

# 3. 手动测试健康检查目标
time curl -x socks5://proxy-node:port https://www.google.com
```

**优化建议**:
- 调整健康检查超时时间（当前可能是 5 秒）
- 增加健康检查间隔，减少并发
- 更换健康检查目标 URL（选择响应更快的服务）

---

## 加载告警规则到 Prometheus

### 方法 1: 修改 prometheus.yml 配置

编辑 `deploy/prometheus/prometheus.yml`，确保包含规则文件路径:

```yaml
rule_files:
  - 'rules/*.yml'
```

### 方法 2: 热重载配置

如果已经在 `rule_files` 中包含了 `rules/*.yml`，只需热重载 Prometheus:

```bash
# 方式 1: HTTP API
curl -X POST http://localhost:9090/-/reload

# 方式 2: 发送 SIGHUP 信号
docker-compose exec prometheus kill -HUP 1

# 方式 3: 重启容器（不推荐）
docker-compose restart prometheus
```

### 验证规则加载成功

```bash
# 1. 检查规则加载状态
curl -s 'http://localhost:9090/api/v1/rules' | jq '.data.groups[] | select(.name=="proxy_alerts")'

# 2. 在 Prometheus UI 查看
# 访问 http://localhost:9090/rules
# 查找 "proxy_alerts" 规则组

# 3. 检查规则语法
promtool check rules deploy/prometheus/rules/proxy-rules.yml
```

---

## 测试验证方法

### 1. 验证指标可用性

```bash
# 检查所有代理相关指标
curl -s 'http://localhost:9090/api/v1/label/__name__/values' | jq '.data[]' | grep proxy

# 检查关键指标当前值
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_dialable' | jq
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_total' | jq
curl -s 'http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_unhealthy' | jq
```

### 2. 模拟告警触发

#### 模拟 ProxyNoDialableNodes

```bash
# 方式 1: 停止所有订阅刷新（如果有控制接口）
curl -X POST http://localhost:8080/admin/proxy/stop-refresh

# 方式 2: 临时禁用所有节点（需要数据库访问）
psql -U postgres -d llm_gateway -c "UPDATE proxy_nodes SET enabled = false;"

# 等待 1 分钟后检查告警
curl -s 'http://localhost:9090/api/v1/alerts' | jq '.data.alerts[] | select(.labels.alertname=="ProxyNoDialableNodes")'
```

#### 模拟 ProxyNodeHealthLow

```bash
# 临时降低告警阈值（编辑 proxy-rules.yml）
# 将 < 0.8 改为 < 0.95，然后重载配置
sed -i 's/< 0.8/< 0.95/' deploy/prometheus/rules/proxy-rules.yml
curl -X POST http://localhost:9090/-/reload

# 恢复阈值
sed -i 's/< 0.95/< 0.8/' deploy/prometheus/rules/proxy-rules.yml
curl -X POST http://localhost:9090/-/reload
```

### 3. 端到端测试

```bash
# 1. 确认告警规则加载
promtool check rules deploy/prometheus/rules/proxy-rules.yml

# 2. 确认指标正常采集
curl -s 'http://localhost:9090/api/v1/query?query=up{job="llm-gateway"}' | jq

# 3. 手动发送测试告警到 Alertmanager
curl -X POST http://localhost:9093/api/v1/alerts \
  -H "Content-Type: application/json" \
  -d '[
    {
      "labels": {
        "alertname": "ProxyNoDialableNodes",
        "severity": "critical",
        "component": "proxy"
      },
      "annotations": {
        "summary": "代理系统无可拨号节点（测试告警）",
        "description": "这是一条测试告警，验证飞书通知是否正常"
      }
    }
  ]'

# 4. 检查飞书群是否收到告警
```

---

## 告警级别定义

| 级别 | 含义 | 响应时间 | 通知渠道 |
|------|------|---------|---------|
| **critical** | 业务严重影响，需要立即处理 | 5 分钟内 | 飞书 + 电话 + 短信 |
| **warning** | 潜在问题，需要关注和处理 | 30 分钟内 | 飞书 |
| **info** | 性能或运维信息，正常工作时间处理 | 4 小时内 | 飞书（低优先级） |

---

## 指标依赖关系

本告警规则依赖以下 Prometheus 指标（由 `proxy/metrics.go` 暴露）:

| 指标名称 | 类型 | 说明 |
|---------|------|------|
| `llm_gateway_proxy_nodes_total` | Gauge | 代理节点总数 |
| `llm_gateway_proxy_nodes_dialable` | Gauge | 可拨号节点数 |
| `llm_gateway_proxy_nodes_unhealthy` | Gauge | 不健康节点数 |
| `llm_gateway_proxy_subscription_refresh_total` | Counter | 订阅刷新次数 (labels: status) |
| `llm_gateway_proxy_subscription_refresh_duration_seconds` | Histogram | 订阅刷新耗时 |
| `llm_gateway_proxy_node_health_check_total` | Counter | 健康检查次数 (labels: status) |
| `llm_gateway_proxy_node_health_check_duration_seconds` | Histogram | 健康检查耗时 |
| `llm_gateway_proxy_node_response_time_ms` | Histogram | 节点响应时间 |
| `llm_gateway_proxy_node_selection_total` | Counter | 节点选择次数 (labels: result) |
| `llm_gateway_proxy_password_decrypt_failed_total` | Counter | 密码解密失败次数 |
| `llm_gateway_proxy_transport_invalidations_total` | Counter | Transport 失效次数 |

---

## 常见问题

### Q1: 告警规则不生效，为什么？

**排查步骤**:
1. 检查规则是否成功加载: `curl http://localhost:9090/api/v1/rules`
2. 检查规则语法: `promtool check rules proxy-rules.yml`
3. 检查指标是否存在: `curl http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_total`
4. 检查查询是否返回结果: 在 Prometheus UI 手动执行 PromQL

### Q2: 告警持续触发但已经恢复，如何处理？

告警有 `for` 持续时间要求，必须持续满足条件才会解除。检查:
1. 指标当前值: 是否真的恢复到正常范围
2. 告警状态: `curl http://localhost:9093/api/v2/alerts`
3. 手动静默: 在 Alertmanager UI 创建 Silence

### Q3: 如何调整告警阈值？

编辑 `proxy-rules.yml`，修改 `expr` 中的阈值，然后重载配置:
```bash
vi deploy/prometheus/rules/proxy-rules.yml
curl -X POST http://localhost:9090/-/reload
```

### Q4: 如何临时禁用某个告警？

**方式 1**: 在 Alertmanager UI 创建 Silence

**方式 2**: 注释掉规则并重载
```bash
# 在规则前添加 # 注释
sed -i 's/- alert: ProxyNoDialableNodes/# - alert: ProxyNoDialableNodes/' proxy-rules.yml
curl -X POST http://localhost:9090/-/reload
```

---

## 相关文档

- [Prometheus 查询语言 (PromQL)](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [Alertmanager 配置](../alertmanager.yml)
- [代理子系统指标定义](../../proxy/metrics.go)
- [LLM Gateway 监控部署指南](../README.md)

---

**维护者**: Infrastructure Team  
**最后更新**: 2026-08-29  
**版本**: v1.0
