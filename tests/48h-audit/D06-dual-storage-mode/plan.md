# D06 — 双模式存储

> 域知识库：[docs/audit/playbook/domains/D06-dual-storage-mode.md](../../../docs/audit/playbook/domains/D06-dual-storage-mode.md)
> R73 改动面：lite telemetry sink 并发落库与降级；full PG/Redis 只做静态核对。
> 状态：历史修复报告 + R73 定向验证；full 集成环境未提供。

## 1. 审计要点

- 核对 full PG+Redis 与 lite SQLite+memory+files 的写入优先级、回填、幂等与降级边界。
- lite sink 的 concurrent journal reservation、idempotent upsert、full journal persistence 已通过 race 定向测试。
- 未覆盖：PG/Redis 真连接、跨进程崩溃恢复和部署启动链。

## 2. 业务测试

- [x] B-01：`go test -race ./cmd/gateway -run 'TestLiteRequestLogSink_' -count=1`

## 3. 数据测试

- [ ] D-01：PG/Redis schema 与实际回填（未验证）

## 4. 压力测试

- [x] S-01：lite concurrent replay race 通过

## 5. 安全测试

- [x] SF-01：降级路径不改变租户/请求身份字段的静态核对

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D06-dual-storage-mode/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-48h-audit-report.md` §F02

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D06-dual-storage-mode/）。
知识库入口：docs/audit/playbook/domains/D06-dual-storage-mode.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
