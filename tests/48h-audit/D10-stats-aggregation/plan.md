# D10 — 统计聚合

> 域知识库：[docs/audit/playbook/domains/D10-stats-aggregation.md](../../../docs/audit/playbook/domains/D10-stats-aggregation.md)
> R73 改动面：lite telemetry turn/journal 幂等、压缩与 provider-window telemetry 归档字段。
> 状态：R73 静态 + 定向证据；真实 PG 聚合/重算未验证。

## 1. 审计要点

- 核对实时/日汇总、补偿、窗口边界、usage/费用和 compression provenance 是否使用同一 request identity。
- lite telemetry 持久化字段和并发 journal 去重已通过定向 race；F03/F04 结果体保留原始/压缩边界。
- 未覆盖：真实 PG 分区读面、跨日重算、provider billing 对账。

## 2. 业务测试

- [x] B-01：lite journal idempotent persistence 定向测试

## 3. 数据测试

- [ ] D-01：PG 日汇总/重算（未连接）

## 4. 压力测试

- [x] S-01：lite sink race 通过

## 5. 安全测试

- [x] SF-01：telemetry 不将保留 rate-limit scope header 写入上游/客户端

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D10-stats-aggregation/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-48h-audit-report.md` §F02/F04/F06

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D10-stats-aggregation/）。
知识库入口：docs/audit/playbook/domains/D10-stats-aggregation.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
