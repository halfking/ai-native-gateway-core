# D16 — 流程闭环

> 域知识库：[docs/audit/playbook/domains/D16-flow-closure.md](../../../docs/audit/playbook/domains/D16-flow-closure.md)
> R73 改动面：请求进入→上游→stream capture→telemetry、压缩恢复和 migration/启动登记。
> 状态：R73 本地闭环证据；PG/Redis/provider/deployment 未完整验证。

## 1. 审计要点

- 核对上传/解析/存储/版本、异步任务、重试/补偿、部署/迁移/启动/停机的调用闭环。
- F03 修复显式 session stream capture context；F04 复用内部恢复；F07 installer/embed/caller 形状已核对。
- 未覆盖：真实 DB migration、部署启动和供应商端到端。

## 2. 业务测试

- [x] B-01：OpenAI/Anthropic/Responses/Ollama stream regression

## 3. 数据测试

- [ ] D-01：兼容 PG migration 754（未连接）

## 4. 压力测试

- [ ] S-01：本地部署/长流压测（待执行）

## 5. 安全测试

- [x] SF-01：取消、scope header 与原文 provenance 边界静态核验

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D16-flow-closure/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-48h-audit-report.md` §F03/F04/F07

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D16-flow-closure/）。
知识库入口：docs/audit/playbook/domains/D16-flow-closure.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
