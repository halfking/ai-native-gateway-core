# Proxy 监控与运维手册

本文只描述代理监控资产与不依赖高基数标签的排障流程。规则入口是 [`deploy/prometheus/rules/proxy-rules.yml`](../../deploy/prometheus/rules/proxy-rules.yml)，Grafana dashboard 位于 [`deploy/grafana`](../../deploy/grafana)，Compose provisioning 配置在 [`deploy/prometheus/docker-compose.yml`](../../deploy/prometheus/docker-compose.yml)。

## 1. 监控契约

所有代理指标使用 `llm_gateway_proxy_` 前缀。标签必须保持低基数：

| 指标 | 类型 | 允许标签 |
|---|---|---|
| `llm_gateway_proxy_subscriptions_total` | gauge | `state=active\|inactive` |
| `llm_gateway_proxy_subscription_refresh_total` | counter | `status=success\|failed` |
| `llm_gateway_proxy_node_health_check_total` | counter | `status=success\|failed`；超时计入 `failed` |
| `llm_gateway_proxy_node_selection_total` | counter | `result=success\|no_dialable\|no_available\|store_error` |

订阅刷新相关指标不带订阅 ID。所有代理监控指标均不得带 `node_id`、`subscription_id`、`location`、`error_type` 或 URL 标签。健康检查失败原因仅在应用日志中排查，不创建 Prometheus 标签。

无标签的核心 gauges/histograms 包括：

- `llm_gateway_proxy_nodes_total`
- `llm_gateway_proxy_nodes_dialable`
- `llm_gateway_proxy_nodes_unhealthy`
- `llm_gateway_proxy_subscription_refresh_duration_seconds`
- `llm_gateway_proxy_node_health_check_duration_seconds`
- `llm_gateway_proxy_node_selection_duration_seconds`
- `llm_gateway_proxy_node_response_time_ms`
- `llm_gateway_proxy_node_consecutive_failures`
- `llm_gateway_proxy_transport_cache_size`
- `llm_gateway_proxy_transport_invalidations_total`

## 2. 常用查询

```promql
# 当前订阅状态
llm_gateway_proxy_subscriptions_total{state="active"}
llm_gateway_proxy_subscriptions_total{state="inactive"}

# 节点健康率；空节点池显示 0，不产生除零结果
(llm_gateway_proxy_nodes_total - llm_gateway_proxy_nodes_unhealthy)
  / clamp_min(llm_gateway_proxy_nodes_total, 1)

# 可拨号节点占比
llm_gateway_proxy_nodes_dialable / clamp_min(llm_gateway_proxy_nodes_total, 1)

# 刷新成功率；无刷新样本时保持有限值
sum(rate(llm_gateway_proxy_subscription_refresh_total{status="success"}[5m]))
  / clamp_min(sum(rate(llm_gateway_proxy_subscription_refresh_total[5m])), 1e-9)

# 健康检查成功率（超时已归入 failed）
sum(rate(llm_gateway_proxy_node_health_check_total{status="success"}[5m]))
  / clamp_min(sum(rate(llm_gateway_proxy_node_health_check_total[5m])), 1e-9)

# 节点选择非成功比例
sum(rate(llm_gateway_proxy_node_selection_total{result!="success"}[5m]))
  / clamp_min(sum(rate(llm_gateway_proxy_node_selection_total[5m])), 1e-9)

# 直方图 P95：必须保留 le 聚合维度
histogram_quantile(0.95,
  sum by (le) (rate(llm_gateway_proxy_node_health_check_duration_seconds_bucket[5m])))
```

## 3. 告警响应

### 无可拨号节点

`ProxyNoDialableNodes` 只在 `llm_gateway_proxy_nodes_total > 0` 且 `llm_gateway_proxy_nodes_dialable == 0` 时触发。节点总数为零表示没有已导入节点，不触发此告警。

1. 查询 `nodes_total`、`nodes_dialable`、`nodes_unhealthy`，确认是空池、协议不可拨号还是健康故障。
2. 查询 `llm_gateway_proxy_subscription_refresh_total{status="success"}` 与 `{status="failed"}`，确认最近是否成功刷新。
3. 查看应用日志中的订阅解析、存储及节点导入错误；不要把 URL 或节点 ID 写入指标标签。
4. 通过管理 API 检查订阅和节点配置，修复订阅内容或启用可拨号节点。
5. 恢复后确认 `nodes_dialable > 0`，并观察选择 `result="success"` 是否恢复。

### 节点健康率异常

`ProxyNodeHealthCritical`（低于 50%）和 `ProxyNodeHealthLow`（低于 80%）仅对非空节点池计算。

1. 查询健康检查成功/失败速率和 P95 耗时。
2. 检查出口网络、代理供应商和健康检查目标；失败原因在日志中查看。
3. 若为批量故障，切换备用订阅或按既有变更流程禁用故障节点。
4. 恢复后确认失败速率下降、`nodes_unhealthy` 回落，并验证实际代理请求。

### 订阅刷新失败或过慢

`ProxySubscriptionRefreshLow` 使用全局 `status=success|failed` 计数，`ProxySubscriptionRefreshSlow` 使用无订阅 ID 的 duration histogram。

1. 查询近 5 分钟 success/failed 速率和刷新 P95。
2. 检查订阅服务可达性、HTTP 响应、内容格式及数据库写入日志。
3. 手动刷新前确认订阅凭据和目标 URL，不要把 URL 放入 Prometheus label。
4. 修复后观察成功率和节点数量；必要时按变更流程切换备用订阅。

### 节点选择失败

`ProxyNodeSelectionFailureHigh` 聚合 `result!=success`，可按 `result` 查看 `no_dialable`、`no_available`、`store_error` 的分布。

- `no_dialable`: 有节点但没有 Go 可拨号协议。
- `no_available`: 没有可供选择的节点，结合节点 gauges 排查。
- `store_error`: 检查存储/数据库可用性以及应用日志。

### 密码解密、连接池与延迟

- `ProxyPasswordDecryptFailed`: 检查部署的解密密钥、密钥轮换和应用日志；错误详情不作为指标标签。
- `ProxyTransportInvalidationsHigh`: 检查订阅刷新频率、节点配置变更和连接池资源。
- `ProxyHealthCheckSlow` / `ProxySubscriptionRefreshSlow`: 使用 histogram P95/P99 查询，确认网络、目标服务和数据库性能。

## 4. Grafana 与 Prometheus 部署

Prometheus 通过 `prometheus.yml` 的 `/etc/prometheus/rules/*.yml` 加载规则。Grafana 使用默认 file provider；Compose 将 `deploy/grafana/proxy-*-dashboard.json` 以只读方式挂载到该 provider 的目录，因此四个 dashboard 会自动发现。Compose 不新增 Alertmanager receiver。

```bash
# 规则、四个 dashboard 与标签契约静态检查
deploy/prometheus/rules/test-proxy-rules.sh

# 若安装 promtool，额外检查 Prometheus 规则语法
promtool check rules deploy/prometheus/rules/proxy-rules.yml

# 运行中的 Prometheus 重载规则（启用了 web.enable-lifecycle 时）
curl -X POST http://localhost:9090/-/reload
curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[] | select(.name == "proxy_alerts")'
```

## 5. 变更与回滚

1. 修改规则或 dashboard 前运行契约脚本。
2. 在 staging 验证 PromQL 返回有限值，尤其是空节点池和无请求/刷新样本窗口。
3. 通过评审后部署；只读 dashboard mount 不应改为可写。
4. 规则异常时恢复上一版本文件并重载 Prometheus；dashboard 异常时回滚对应 JSON 并等待 provider 扫描。
5. 不在规则文件中写 receiver、外部 webhook 或虚构 runbook URL。告警处理入口为本文件及仓库相对链接。
