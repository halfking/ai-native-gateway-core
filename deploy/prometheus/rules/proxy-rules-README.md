# Proxy Prometheus 规则与指标契约

规则文件为 [`proxy-rules.yml`](proxy-rules.yml)，由 `prometheus.yml` 的 `/etc/prometheus/rules/*.yml` 加载。Grafana dashboard 通过 [`deploy/prometheus/docker-compose.yml`](../../prometheus/docker-compose.yml) 的只读挂载自动 provisioning。

## 最终指标契约

代理指标必须保持低基数，且只能使用以下枚举标签：

| 指标 | 类型 | 标签与允许值 |
|---|---|---|
| `llm_gateway_proxy_subscriptions_total` | gauge | `state=active\|inactive` |
| `llm_gateway_proxy_subscription_refresh_total` | counter | `status=success\|failed`，不含订阅 ID |
| `llm_gateway_proxy_node_health_check_total` | counter | `status=success\|failed`；超时归入 `failed` |
| `llm_gateway_proxy_node_selection_total` | counter | `result=success\|no_dialable\|no_available\|store_error` |

其他代理 gauge/counter/histogram 不带 `node_id`、`subscription_id`、`location`、`error_type` 或 URL 标签。订阅刷新 duration histogram 同样不带订阅 ID。

## 告警规则

- `ProxyNoDialableNodes`: `nodes_total > 0` 且 `nodes_dialable == 0`。空节点池不会触发该告警。
- `ProxyNodeHealthCritical` / `ProxyNodeHealthLow`: 仅在 `nodes_total > 0` 时计算比例，并使用 `clamp_min` 防止零除。
- `ProxySubscriptionRefreshLow`: 聚合全局刷新 counter，失败状态使用 `status="failed"`，并在无刷新样本时不触发。
- `ProxyNodeSelectionFailureHigh`: 将所有非 success 结果聚合后计算比例，在无请求样本时不触发。
- 延迟告警使用 `histogram_quantile` 配合 `sum by (le) (rate(..._bucket[5m]))`。

告警 annotation 的 runbook 使用仓库相对路径，主手册为 [`docs/operations/proxy-ops-manual.md`](../../../docs/operations/proxy-ops-manual.md)。本规则文件不配置 Alertmanager receiver。

## 校验

在仓库根目录执行：

```bash
deploy/prometheus/rules/test-proxy-rules.sh
```

脚本会执行 YAML、dashboard JSON、低基数标签/PromQL 静态契约检查；若本机安装了 `promtool`，也会执行 `promtool check rules`。Prometheus 运行时可用以下命令检查加载状态：

```bash
curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[] | select(.name == "proxy_alerts")'
```
