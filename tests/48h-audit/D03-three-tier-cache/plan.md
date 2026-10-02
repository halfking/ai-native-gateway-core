# D03 — 三层缓存 provenance

> 域知识库：[docs/audit/playbook/domains/D03-three-tier-cache.md](../../../docs/audit/playbook/domains/D03-three-tier-cache.md)
> R73 改动面：lite telemetry 并发幂等、流式 request/response capture、压缩 provenance。
> 状态：R73 续审记录；R78 验收门已改指真实证据所在包（原门匹配 0 包，永不可能失败）；PG/Redis 跨进程一致性未验证。

## 1. 审计要点

- 检查 original/sanitized/compressed identity、occurrence 与 provenance；重点代码：`domains/hooks/compression/`、`cmd/gateway/lite_telemetry_sink.go`。
- 已有定向证据：lite sink 并发 replay/idempotent upsert race 通过；F03 reader context 回归通过。
- 未覆盖：跨进程原子 offset、真实 Redis/PG 回填与故障恢复。

## 2. 业务测试

- [x] B-01：`go test -race ./cmd/gateway -run 'TestLiteRequestLogSink_' -count=1`（证据在 ./cmd/gateway，R78 已把验收门接到该处）

## 3. 数据测试

- [ ] D-01：兼容 PG/Redis 上验证跨进程回填（环境未提供）

## 4. 压力测试

- [x] S-01：lite sink 定向 race 已通过；另 §3 清单第 1 条（三层 provenance 完整 + occurrence 回溯 + 映射缺口不静默）由 `./domains/hooks/compression` 的 9 条 alignment 测试覆盖，R78 起纳入验收门

## 5. 安全测试

- [ ] SF-01：本轮未做日志捕获验证；原文 provenance 仅由代码路径核对，不能据此宣称原文绝不进入日志

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D03-three-tier-cache/...` 匹配 0 个包——Go 的空包
# 模式只打印 `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的
# provenance 证据本来就在 compression 包里（alignment.go 的 buildAlignmentMap +
# alignment_test.go 9 条），旧门只是没接到它。门已改指真实证据所在包。
go test -race -timeout 120s ./domains/hooks/compression/... -count=1
go test -race -timeout 120s ./cmd/gateway -run 'TestLiteRequestLogSink_' -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-48h-audit-report.md` §F02/F03；跨进程与真库保持未验证

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D03-three-tier-cache/）。
知识库入口：docs/audit/playbook/domains/D03-three-tier-cache.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
