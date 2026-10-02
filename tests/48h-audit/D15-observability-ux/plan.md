# D15 — 可观测性 + UX

> 域知识库：[docs/audit/playbook/domains/D15-observability-ux.md](../../../docs/audit/playbook/domains/D15-observability-ux.md)
> R73 改动面：lite telemetry、压缩/stream lifecycle 事件、错误与限流展示口径。
> 状态：R73 定向证据；真实 dashboard/多语言浏览器验收未执行。

## 1. 审计要点

- 核对 status、dashboard、credential/session 视图是否使用统一 request identity、compression strategy 和 error kind。
- F02/F03/F04/F06 已补齐关键 capture/错误阶段语义；未宣称前端端到端完成。
- 未覆盖：真实 API/UI、i18n 浏览器和完整 telemetry pipeline。

## 2. 业务测试

- [x] B-01：lite persistence 与 stream lifecycle 定向回归

## 3. 数据测试

- [ ] D-01：dashboard/PG 聚合读面（未验证）

## 4. 压力测试

- [ ] S-01：浏览器/长流 UX 压测（未执行）

## 5. 安全测试

- [x] SF-01：错误/限流 scope 不泄漏保留内部 header

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D15-observability-ux/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
观测与 UX 的服务端侧：证据在 ./telemetry 与 ./metrics 的遥测回归（UI 侧仍需 D-01/S-01 真环境，本门不覆盖）。
go test -race -timeout 120s ./telemetry/... ./metrics/... -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-remediation-plan.md`

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D15-observability-ux/）。
知识库入口：docs/audit/playbook/domains/D15-observability-ux.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
