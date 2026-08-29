# 成功请求缺少响应正文：监控与历史扫描

## SSOT

唯一告警指标是：

```promql
telemetry_success_response_body_missing_total{protocol,stream}
```

该指标由 settlement telemetry 在成功持久化后触发，语义以 ADR
[`2026-08-29-success-empty-response-body.md`](../../../adr/2026-08-29-success-empty-response-body.md)
为准：成功、response body 为 nil 或全空白、且没有 stream chunks。

- 仅 `stream="non_stream"` 是告警面。
- `stream="stream"` 仅供比例分析；流式字节可能位于 stream capture/outbound body，空 `response_body` 不是告警条件。
- 不新增同义的 `llm_gateway_*` counter，不用 tenant、request ID、body 或 model 作为 metric label。
- streaming handler 中的 `empty_response_body` quality flag 对 `{}` 的分类可能不同；它不是本告警的 SSOT，任何统一语义的改动必须先更新 ADR 和 telemetry tests。

## Alert

`deploy/prometheus/rules/alerts.yml` 中的
`GatewaySuccessfulNonStreamResponseBodyMissing` 在五分钟窗口有任意增量并持续五分钟后以 warning 触发。

本地变更后可执行：

```bash
cd deploy/prometheus/rules
go test ./...
# 若本机安装 promtool：
promtool check rules alerts.yml
```

Prometheus/Grafana reload、真实 alert firing 和生产阈值调整仅在授权环境执行，并保存 commit/version/UTC、脱敏查询结果和 rule reload 证据。

## 有界历史扫描设计

历史扫描只用于发现持久化遗漏，不回写 request logs、body hot 表、columnar parent 或任何历史分区。

建议查询边界：

1. 每次最多扫描一个固定 UTC 时间窗，例如最近 15 分钟；
2. 以 `request_logs` 的成功记录为 source，left join body metadata/availability；
3. 聚合为总数和 bounded protocol/stream buckets；
4. 限制执行频率（例如每 15 分钟一次）并设置 query timeout；
5. 结果输出到 logs 或独立低基数 metric，不能逐 request 写 Prometheus labels；
6. 先在隔离 PG 用 EXPLAIN ANALYZE 验证索引和预算，再决定是否接入 worker。

扫描不能替代实时 counter；实时 counter 是低延迟告警面，扫描只用于审计和回填发现。

## 真实环境证据

在授权 245/154 环境中，验证：

- `/metrics` 中存在预初始化 protocol/stream series；
- 非流式成功请求无缺失时 non_stream 增量为 0；
- 经过批准的诊断样本可产生预期 warning/counter，但不得破坏真实客户数据；
- alert rule 已加载，dashboard 使用同一 counter；
- 旧质量 flag 与 telemetry counter 的 `{}` 语义差异已在运行手册中保留。
